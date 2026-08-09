package simfile

import "errors"

type SMSC string

func (smsc SMSC) String() string {
	return string(smsc)
}

func (smsc SMSC) MarshalText() ([]byte, error) {
	return []byte(string(smsc)), nil
}

func (smsc *SMSC) UnmarshalText(text []byte) error {
	*smsc = SMSC(string(text))
	return nil
}

func (smsc *SMSC) UnmarshalBinary(data []byte) error {
	y := len(data) - 28
	if y < 0 || y+26 > len(data) {
		return errors.New("reading EF_SMSP: malformed record")
	}

	sca := data[y+13 : y+25]
	if len(sca) < 2 {
		*smsc = ""
		return nil
	}
	length := int(sca[0])
	// The length octet describes bytes after itself, so it must still fit inside
	// the fixed 12-byte SCA field.
	if length <= 1 || length+1 > len(sca) {
		*smsc = ""
		return nil
	}
	if sca[1] != 0x91 {
		*smsc = ""
		return nil
	}

	number, err := decodeSwappedBCD(sca[2:length+1], false)
	if err != nil {
		return err
	}
	if number == "" {
		*smsc = ""
		return nil
	}

	*smsc = SMSC("+" + number)
	return nil
}
