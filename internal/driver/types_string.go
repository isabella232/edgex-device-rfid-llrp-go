// Code generated by "stringer -type=VendorIDType,ImpinjModelType -output types_string.go"; DO NOT EDIT.

package driver

import "strconv"

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	// Re-run the stringer command to generate them again.
	var x [1]struct{}
	_ = x[Impinj-25882]
	_ = x[Alien-17996]
	_ = x[Zebra-10642]
}

const (
	_VendorIDType_name_0 = "Zebra"
	_VendorIDType_name_1 = "Alien"
	_VendorIDType_name_2 = "Impinj"
)

func (i VendorIDType) String() string {
	switch {
	case i == 10642:
		return _VendorIDType_name_0
	case i == 17996:
		return _VendorIDType_name_1
	case i == 25882:
		return _VendorIDType_name_2
	default:
		return "VendorIDType(" + strconv.FormatInt(int64(i), 10) + ")"
	}
}
func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	// Re-run the stringer command to generate them again.
	var x [1]struct{}
	_ = x[SpeedwayR220-2001001]
	_ = x[SpeedwayR420-2001002]
	_ = x[XPortal-2001003]
	_ = x[XArrayWM-2001004]
	_ = x[XArrayEAP-2001006]
	_ = x[XArray-2001007]
	_ = x[XSpan-2001008]
	_ = x[SpeedwayR120-2001009]
	_ = x[R700-2001052]
}

const (
	_ImpinjModelType_name_0 = "SpeedwayR220SpeedwayR420XPortalXArrayWM"
	_ImpinjModelType_name_1 = "XArrayEAPXArrayXSpanSpeedwayR120"
	_ImpinjModelType_name_2 = "R700"
)

var (
	_ImpinjModelType_index_0 = [...]uint8{0, 12, 24, 31, 39}
	_ImpinjModelType_index_1 = [...]uint8{0, 9, 15, 20, 32}
)

func (i ImpinjModelType) String() string {
	switch {
	case 2001001 <= i && i <= 2001004:
		i -= 2001001
		return _ImpinjModelType_name_0[_ImpinjModelType_index_0[i]:_ImpinjModelType_index_0[i+1]]
	case 2001006 <= i && i <= 2001009:
		i -= 2001006
		return _ImpinjModelType_name_1[_ImpinjModelType_index_1[i]:_ImpinjModelType_index_1[i+1]]
	case i == 2001052:
		return _ImpinjModelType_name_2
	default:
		return "ImpinjModelType(" + strconv.FormatInt(int64(i), 10) + ")"
	}
}
