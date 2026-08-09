package simfile

import "errors"

type AdministrativeData struct {
	MNCLength int
}

func (ad *AdministrativeData) UnmarshalBinary(data []byte) error {
	if len(data) < 4 {
		return errors.New("parsing EF_AD: truncated payload")
	}

	switch data[3] & 0x0F {
	case 0x02, 0x03:
		*ad = AdministrativeData{MNCLength: int(data[3] & 0x0F)}
		return nil
	case 0x00:
		return errors.New("parsing EF_AD: MNC length is unavailable")
	default:
		return errors.New("parsing EF_AD: invalid MNC length")
	}
}
