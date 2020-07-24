//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/pkg/errors"
	"github.impcloud.net/RSP-Inventory-Suite/device-llrp-go/internal/llrp"
	"io/ioutil"
	"net"
	"sync"
	"time"

	dsModels "github.com/edgexfoundry/device-sdk-go/pkg/models"
	"github.com/edgexfoundry/go-mod-core-contracts/clients/logger"
	contract "github.com/edgexfoundry/go-mod-core-contracts/models"
)

const (
	ServiceName string = "edgex-device-llrp"

	shutdownGrace       = time.Second // time permitted to Shutdown; if exceeded, Close is called
	closedSenderRetries = 3           // number of times to retry sending a message if it fails due to a closed reader
)

var once sync.Once
var driver *Driver

type Driver struct {
	lc       logger.LoggingClient
	asyncCh  chan<- *dsModels.AsyncValues
	deviceCh chan<- []dsModels.DiscoveredDevice

	activeDevices map[string]*Lurpper
	devicesMu     sync.RWMutex

	svc ServiceWrapper
}

func NewProtocolDriver() dsModels.ProtocolDriver {
	once.Do(func() {
		driver = &Driver{
			activeDevices: make(map[string]*Lurpper),
		}
	})
	return driver
}

func (d *Driver) service() ServiceWrapper {
	if d.svc == nil {
		d.svc = RunningService()
	}
	return d.svc
}

// Initialize performs protocol-specific initialization for the device
// service.
func (d *Driver) Initialize(lc logger.LoggingClient, asyncCh chan<- *dsModels.AsyncValues, deviceCh chan<- []dsModels.DiscoveredDevice) error {
	if lc == nil {
		// prevent panics from this annoyance
		d.lc = logger.NewClientStdOut(ServiceName, false, "DEBUG")
		d.lc.Error("EdgeX initialized us with a nil logger >:(")
	} else {
		d.lc = lc
	}

	d.asyncCh = asyncCh
	d.deviceCh = deviceCh

	go func() {
		// hack: sleep to allow edgex time to finish loading cache and clients
		time.Sleep(5 * time.Second)

		d.addProvisionWatcher()
		// todo: check configuration to make sure discovery is enabled
		d.Discover()
	}()
	return nil
}

type protocolMap = map[string]contract.ProtocolProperties

const (
	ResourceReaderCap          = "ReaderCapabilities"
	ResourceReaderConfig       = "ReaderConfig"
	ResourceReaderNotification = "ReaderEventNotification"
	ResourceROSpec             = "ROSpec"
	ResourceROSpecID           = "ROSpecID"
	ResourceAccessSpec         = "AccessSpec"
	ResourceAccessSpecID       = "AccessSpecID"
	ResourceROAccessReport     = "ROAccessReport"

	ResourceAction = "Action"
	ActionDelete   = "Delete"
	ActionEnable   = "Enable"
	ActionDisable  = "Disable"
	ActionStart    = "Start"
	ActionStop     = "Stop"
)

// HandleReadCommands triggers a protocol Read operation for the specified device.
func (d *Driver) HandleReadCommands(devName string, p protocolMap, reqs []dsModels.CommandRequest) ([]*dsModels.CommandValue, error) {
	d.lc.Debug(fmt.Sprintf("LLRP-Driver.HandleWriteCommands: "+
		"device: %s protocols: %v reqs: %+v", devName, p, reqs))

	if len(reqs) == 0 {
		return nil, errors.New("missing requests")
	}

	dev, err := d.getDevice(devName, p)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	var responses = make([]*dsModels.CommandValue, len(reqs))
	for i := range reqs {
		var llrpReq llrp.Outgoing
		var llrpResp llrp.Incoming

		switch reqs[i].DeviceResourceName {
		case ResourceReaderConfig:
			llrpReq = &llrp.GetReaderConfig{}
			llrpResp = &llrp.GetReaderConfigResponse{}
		case ResourceReaderCap:
			llrpReq = &llrp.GetReaderCapabilities{}
			llrpResp = &llrp.GetReaderCapabilitiesResponse{}
		case ResourceROSpec:
			llrpReq = &llrp.GetROSpecs{}
			llrpResp = &llrp.GetROSpecsResponse{}
		case ResourceAccessSpec:
			llrpReq = &llrp.GetAccessSpecs{}
			llrpResp = &llrp.GetAccessSpecsResponse{}
		}

		if err := dev.TrySend(ctx, llrpReq, llrpResp); err != nil {
			return nil, err
		}

		respData, err := json.Marshal(llrpResp)
		if err != nil {
			return nil, err
		}

		responses[i] = dsModels.NewStringValue(
			reqs[i].DeviceResourceName, time.Now().UnixNano(), string(respData))
	}

	return responses, nil
}

// HandleWriteCommands passes a slice of CommandRequest struct each representing
// a ResourceOperation for a specific device resource.
// Since the commands are actuation commands, params provide parameters for the individual
// command.
func (d *Driver) HandleWriteCommands(devName string, p protocolMap, reqs []dsModels.CommandRequest, params []*dsModels.CommandValue) error {
	d.lc.Debug(fmt.Sprintf("LLRP-Driver.HandleWriteCommands: "+
		"device: %s protocols: %v reqs: %+v", devName, p, reqs))

	if len(reqs) == 0 {
		return errors.New("missing requests")
	}

	dev, err := d.getDevice(devName, p)
	if err != nil {
		return err
	}

	getParam := func(name string, idx int, key string) (*dsModels.CommandValue, error) {
		if idx > len(params) {
			return nil, errors.Errorf("%s needs at least %d parameters, but got %d",
				name, idx, len(params))
		}

		cv := params[idx]
		if cv == nil {
			return nil, errors.Errorf("%s requires parameter %s", name, key)
		}

		if cv.DeviceResourceName != key {
			return nil, errors.Errorf("%s expected parameter %d: %s, but got %s",
				name, idx, key, cv.DeviceResourceName)
		}

		return cv, nil
	}

	getStrParam := func(name string, idx int, key string) (string, error) {
		if cv, err := getParam(name, idx, key); err != nil {
			return "", err
		} else {
			return cv.StringValue()
		}
	}

	getUint32Param := func(name string, idx int, key string) (uint32, error) {
		if cv, err := getParam(name, idx, key); err != nil {
			return 0, err
		} else {
			return cv.Uint32Value()
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	var llrpReq llrp.Outgoing  // the message to send
	var llrpResp llrp.Incoming // the expected response
	var reqData []byte         // incoming JSON request data, if present
	var dataTarget interface{} // used if the reqData in a subfield of the llrpReq

	switch reqs[0].DeviceResourceName {
	case ResourceReaderConfig:
		data, err := getStrParam("Set"+ResourceReaderConfig, 0, ResourceReaderConfig)
		if err != nil {
			return err
		}

		reqData = []byte(data)
		llrpReq = &llrp.SetReaderConfig{}
		llrpResp = &llrp.SetReaderConfigResponse{}
	case ResourceROSpec:
		data, err := getStrParam("Add"+ResourceROSpec, 0, ResourceROSpec)
		if err != nil {
			return err
		}

		reqData = []byte(data)

		addSpec := llrp.AddROSpec{}
		dataTarget = &addSpec.ROSpec // the incoming data is an ROSpec, not AddROSpec
		llrpReq = &addSpec           // but we want to send AddROSpec, not just ROSpec
		llrpResp = &llrp.AddROSpecResponse{}
	case ResourceROSpecID:
		if len(params) != 2 {
			return errors.Errorf("expected 2 resources for ROSpecID op, but got %d", len(params))
		}

		action, err := getStrParam(ResourceROSpec, 1, ResourceAction)
		if err != nil {
			return err
		}

		roID, err := getUint32Param(action+ResourceROSpec, 0, ResourceROSpecID)
		if err != nil {
			return err
		}

		switch action {
		default:
			return errors.Errorf("unknown ROSpecID action: %q", action)
		case ActionEnable:
			llrpReq = &llrp.EnableROSpec{ROSpecID: roID}
			llrpResp = &llrp.EnableROSpecResponse{}
		case ActionStart:
			llrpReq = &llrp.StartROSpec{ROSpecID: roID}
			llrpResp = &llrp.StartROSpecResponse{}
		case ActionStop:
			llrpReq = &llrp.StopROSpec{ROSpecID: roID}
			llrpResp = &llrp.StopROSpecResponse{}
		case ActionDisable:
			llrpReq = &llrp.DisableROSpec{ROSpecID: roID}
			llrpResp = &llrp.DisableROSpecResponse{}
		case ActionDelete:
			llrpReq = &llrp.DeleteROSpec{ROSpecID: roID}
			llrpResp = &llrp.DeleteROSpecResponse{}
		}

	case ResourceAccessSpecID:
		if len(reqs) != 2 {
			return errors.Errorf("expected 2 resources for AccessSpecID op, but got %d", len(reqs))
		}

		action := reqs[1].DeviceResourceName

		asID, err := getUint32Param(action+ResourceAccessSpecID, 0, ResourceAccessSpecID)
		if err != nil {
			return err
		}

		switch action {
		default:
			return errors.Errorf("unknown ROSpecID action: %q", action)
		case ActionEnable:
			llrpReq = &llrp.EnableAccessSpec{AccessSpecID: asID}
			llrpResp = &llrp.EnableAccessSpecResponse{}
		case ActionDisable:
			llrpReq = &llrp.DisableAccessSpec{AccessSpecID: asID}
			llrpResp = &llrp.DisableAccessSpecResponse{}
		case ActionDelete:
			llrpReq = &llrp.DeleteAccessSpec{AccessSpecID: asID}
			llrpResp = &llrp.DeleteAccessSpecResponse{}
		}
	}

	if reqData != nil {
		if dataTarget != nil {
			if err := json.Unmarshal(reqData, dataTarget); err != nil {
				return errors.Wrap(err, "failed to unmarshal request")
			}
		} else {
			if err := json.Unmarshal(reqData, llrpReq); err != nil {
				return errors.Wrap(err, "failed to unmarshal request")
			}
		}
	}

	// SendFor will handle turning ErrorMessages and failing LLRPStatuses into errors.
	if err := dev.TrySend(ctx, llrpReq, llrpResp); err != nil {
		return err
	}

	go func(resName, devName string, resp llrp.Incoming) {
		respData, err := json.Marshal(resp)
		if err != nil {
			d.lc.Error("failed to marshal response", "message", resName, "error", err)
			return
		}

		cv := dsModels.NewStringValue(resName, time.Now().UnixNano(), string(respData))
		d.asyncCh <- &dsModels.AsyncValues{
			DeviceName:    devName,
			CommandValues: []*dsModels.CommandValue{cv},
		}
	}(reqs[0].DeviceResourceName, dev.name, llrpResp)

	return nil
}

// Stop the protocol-specific DS code to shutdown gracefully, or
// if the force parameter is 'true', immediately. The driver is responsible
// for closing any in-use channels, including the channel used to send async
// readings (if supported).
func (d *Driver) Stop(force bool) error {
	// Then Logging Client might not be initialized
	if d.lc == nil {
		d.lc = logger.NewClientStdOut(ServiceName, false, "DEBUG")
		d.lc.Error("EdgeX called Stop without calling Initialize >:(")
	}
	d.lc.Debug("LLRP-Driver.Stop called", "force", force)

	d.devicesMu.Lock()
	defer d.devicesMu.Unlock()

	ctx := context.Background()

	var wg *sync.WaitGroup
	if !force {
		wg = new(sync.WaitGroup)
		wg.Add(len(d.activeDevices))
		defer wg.Wait()

		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, shutdownGrace)
		defer cancel()
	}

	for _, dev := range d.activeDevices {
		go func(dev *Lurpper) {
			d.stopDevice(ctx, dev)
			if !force {
				wg.Done()
			}
		}(dev)
	}

	d.activeDevices = make(map[string]*Lurpper)
	return nil
}

// AddDevice is a callback function that is invoked
// when a new Device associated with this Device Service is added
func (d *Driver) AddDevice(deviceName string, protocols protocolMap, adminState contract.AdminState) error {
	d.lc.Debug(fmt.Sprintf("Adding new device: %s protocols: %v adminState: %v",
		deviceName, protocols, adminState))
	_, err := d.getDevice(deviceName, protocols)
	return err
}

// UpdateDevice is a callback function that is invoked
// when a Device associated with this Device Service is updated
func (d *Driver) UpdateDevice(deviceName string, protocols protocolMap, adminState contract.AdminState) error {
	d.lc.Debug(fmt.Sprintf("Updating device: %s protocols: %v adminState: %v",
		deviceName, protocols, adminState))

	dev, err := d.getDevice(deviceName, protocols)
	if err != nil {
		return err
	}

	addr, err := getAddr(protocols)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	return dev.UpdateAddr(ctx, addr)
}

// RemoveDevice is a callback function that is invoked
// when a Device associated with this Device Service is removed
func (d *Driver) RemoveDevice(deviceName string, p protocolMap) error {
	d.lc.Debug(fmt.Sprintf("Removing device: %s protocols: %v", deviceName, p))

	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	d.removeDevice(ctx, deviceName)
	return nil
}

// getOrCreate returns a Client, creating one if needed.
//
// If a Client with this name already exists, it returns it.
// Otherwise, calls the createNew function to get a new Client,
// which it adds to the map and then returns.
func (d *Driver) getDevice(name string, p protocolMap) (*Lurpper, error) {
	// Try with just a read lock.
	d.devicesMu.RLock()
	c, ok := d.activeDevices[name]
	d.devicesMu.RUnlock()
	if ok {
		return c, nil
	}

	addr, err := getAddr(p)
	if err != nil {
		return nil, err
	}

	// It's important it holds the lock while creating a device.
	// If two requests arrive at about the same time and target the same device,
	// one will block waiting for the lock and the other will create/add it.
	// When gaining the lock, we recheck the map
	// This way, only one device exists for any name,
	// and all requests that target it use the same one.
	d.devicesMu.Lock()
	defer d.devicesMu.Unlock()

	dev, ok := d.activeDevices[name]
	if ok {
		return dev, nil
	}

	dev = d.NewLurpper(name, addr)
	d.activeDevices[name] = dev
	return dev, nil
}

// removeDevice deletes a device from the active devices map
// and shuts down its client connection to an LLRP device.
func (d *Driver) removeDevice(ctx context.Context, deviceName string) {
	d.devicesMu.Lock()
	defer d.devicesMu.Unlock()

	if dev, ok := d.activeDevices[deviceName]; ok {
		go d.stopDevice(ctx, dev)
		delete(d.activeDevices, deviceName)
	}
}

// stopDevice stops a device's reconnect loop,
// closing any active connection it may currently have.
// Any pending requests targeting that device may fail.
// This doesn't remove it from the devices map.
func (d *Driver) stopDevice(ctx context.Context, dev *Lurpper) {
	if err := dev.Stop(ctx); err != nil {
		d.lc.Error("error attempting client shutdown", "error", err.Error())
	}
}

// getAddr extracts an address from a protocol mapping.
//
// It expects the map to have {"tcp": {"host": "<ip>", "port": "<port>"}}.
func getAddr(protocols protocolMap) (net.Addr, error) {
	tcpInfo := protocols["tcp"]
	if tcpInfo == nil {
		return nil, errors.New("missing tcp protocol")
	}

	host := tcpInfo["host"]
	port := tcpInfo["port"]
	if host == "" || port == "" {
		return nil, errors.Errorf("tcp missing host or port (%q, %q)", host, port)
	}

	addr, err := net.ResolveTCPAddr("tcp", host+":"+port)
	return addr, errors.Wrapf(err,
		"unable to create addr for tcp protocol (%q, %q)", host, port)
}

func (d *Driver) addProvisionWatcher() error {
	var provisionWatcher contract.ProvisionWatcher
	data, err := ioutil.ReadFile("res/provisionwatcher.json")
	if err != nil {
		d.lc.Error(err.Error())
		return err
	}

	err = provisionWatcher.UnmarshalJSON(data)
	if err != nil {
		d.lc.Error(err.Error())
		return err
	}

	if err := d.service().AddOrUpdateProvisionWatcher(provisionWatcher); err != nil {
		d.lc.Info(err.Error())
		return err
	}

	return nil
}

func (d *Driver) Discover() {
	d.lc.Info("*** Discover was called ***")
	d.deviceCh <- autoDiscover()
	d.lc.Info("scanning complete")
}
