# uicc-go

`uicc-go` is a Go protocol library for working with UICC, USIM, ISIM, and SIM access paths.

The repository separates protocol packages from USIM business logic:

- Top-level protocol packages implement concrete primitives such as AT `+CSIM`, PC/SC CCID APDU transport, MBIM UICC low-level access, QMI UIM, and QRTR QMI transport.
- `apdu` implements APDU request/response encoding.
- `usim` adapts readers into higher-level USIM and ISIM operations such as loading ICCID/IMSI identities, AKA, EAP-AKA, and SMS-PP download.

The protocol readers do not depend on `usim`. Use `usim` only when you want card-level business behavior on top of a reader.

## Status

This is an early protocol extraction layer. The current focus is correctness, explicit transport selection, and small idiomatic APIs.

Implemented reader families:

- AT APDU transport over serial ports with `AT+CSIM`
- CCID APDU transport over PC/SC
- MBIM direct and proxy transports with UICC low-level access
- QMI direct and proxy transports
- QRTR direct Linux socket transport
- QCOM UIM primitives over QMI or QRTR

The implementation is pure Go. It does not use cgo and does not link against `libqmi`, `libmbim`, or `libqrtr-glib`.

## Requirements

- Go 1.26 or newer
- Linux for direct `/dev/cdc-wdm*` QMI/MBIM and QRTR socket support
- Linux or Windows for AT serial support
- PC/SC runtime for `ccid`

Install:

```sh
go get github.com/damonto/uicc-go
```

## Package Layout

```text
apdu                         APDU request/response encoding
at                           AT +CSIM APDU reader
ccid                         PC/SC CCID APDU reader
cdcwdm                       Linux cdc-wdm connection primitive
mbim                         MBIM protocol, proxy/direct dialers, UICC access
qcom                         Shared QCOM QMI/QMUX constants and transport contracts
qcom/qmi                     QMI/QMUX transport, proxy/direct dialers
qcom/qrtr                    QRTR transport for QMI services
qcom/tlv                     QCOM QMI TLV types, codecs, constructors, and lookup helpers
qcom/uim                     QMI UIM primitives
usim                         USIM/ISIM card loading and high-level operations
usim/card                    Card-facing interfaces consumed by usim
usim/command                 APDU command helpers used by usim
usim/simfile                 SIM file parsers
usim/tlv                     BER-TLV helpers
```

## Transport Model

`qmi`, `mbim`, and `qrtr` separate protocol logic from connection setup.

QMI and MBIM require an explicit dialer option:

```go
qmi.Open(ctx, qmi.WithProxy("/dev/cdc-wdm0"))
qmi.Open(ctx, qmi.WithDirect("/dev/cdc-wdm0"))

mbim.Open(ctx, mbim.WithProxy("/dev/cdc-wdm0"), mbim.WithSlot(1))
mbim.Open(ctx, mbim.WithDirect("/dev/cdc-wdm0"), mbim.WithSlot(1))
```

Proxy mode connects to the existing daemon socket protocol (`qmi-proxy` or `mbim-proxy`) and passes the device path through that proxy protocol.

Direct mode opens the device node and performs framing in Go.

QRTR is a Linux QRTR socket transport:

```go
transport, err := qrtr.Open(ctx)
```

## APDU Readers

AT and CCID expose the same APDU-style shape:

```go
type Transmitter interface {
	Transmit(ctx context.Context, req []byte) ([]byte, error)
	Close() error
}
```

### AT

```go
package main

import (
	"context"
	"log"

	"github.com/damonto/uicc-go/at"
)

func main() {
	ctx := context.Background()

	reader, err := at.Open("/dev/ttyUSB2", 115200)
	if err != nil {
		log.Fatal(err)
	}
	defer reader.Close()

	resp, err := reader.Transmit(ctx, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x3F, 0x00})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("% X", resp)
}
```

### CCID

```go
package main

import (
	"context"
	"log"

	"github.com/damonto/uicc-go/ccid"
)

func main() {
	ctx := context.Background()

	names, err := ccid.ListReaders(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if len(names) == 0 {
		log.Fatal("no PC/SC readers")
	}

	reader, err := ccid.Open(ctx, names[0])
	if err != nil {
		log.Fatal(err)
	}
	defer reader.Close()

	resp, err := reader.Transmit(ctx, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x3F, 0x00})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("% X", resp)
}
```

## QCOM UIM over QMI

Use `qmi.Open` to create a QMI transport, then `uim.New` to allocate a UIM client and expose QMI UIM primitives.

```go
package main

import (
	"context"
	"log"

	"github.com/damonto/uicc-go/qcom/qmi"
	"github.com/damonto/uicc-go/qcom/uim"
)

func main() {
	ctx := context.Background()

	transport, err := qmi.Open(ctx, qmi.WithProxy("/dev/cdc-wdm0"))
	if err != nil {
		log.Fatal(err)
	}

	reader, err := uim.New(ctx, transport, uim.WithSlot(1))
	if err != nil {
		log.Fatal(err)
	}
	defer reader.Close()

	status, err := reader.CardStatus(ctx)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("ready=%v", status.Ready())
}
```

Direct mode:

```go
transport, err := qmi.Open(ctx, qmi.WithDirect("/dev/cdc-wdm0"))
```

## QCOM UIM over QRTR

QRTR uses the top-level `qcom/qrtr` package and is not nested under `qmi`.

```go
package main

import (
	"context"
	"log"

	"github.com/damonto/uicc-go/qcom/qrtr"
	"github.com/damonto/uicc-go/qcom/uim"
)

func main() {
	ctx := context.Background()

	transport, err := qrtr.Open(ctx)
	if err != nil {
		log.Fatal(err)
	}

	reader, err := uim.New(ctx, transport, uim.WithSlot(1))
	if err != nil {
		log.Fatal(err)
	}
	defer reader.Close()

	if err := reader.PowerOffSIM(ctx, 1); err != nil {
		log.Fatal(err)
	}
	if err := reader.PowerOnSIM(ctx, uim.PowerOnSIMRequest{Slot: 1}); err != nil {
		log.Fatal(err)
	}
}
```

## MBIM UICC Low-Level Access

MBIM exposes UICC primitives directly from `mbim`.

```go
package main

import (
	"context"
	"log"

	"github.com/damonto/uicc-go/mbim"
)

func main() {
	ctx := context.Background()

	reader, err := mbim.Open(ctx, mbim.WithProxy("/dev/cdc-wdm0"), mbim.WithSlot(1))
	if err != nil {
		log.Fatal(err)
	}
	defer reader.Close()

	apps, err := reader.ListApplications(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, app := range apps {
		log.Printf("% X %s", app.AID, app.Label)
	}
}
```

Direct mode:

```go
reader, err := mbim.Open(ctx, mbim.WithDirect("/dev/cdc-wdm0"), mbim.WithSlot(1))
```

## USIM Adaptation

`usim` consumes small card-facing interfaces from `usim/card`. It can work over AT, CCID, QMI UIM, or MBIM after adaptation.

APDU transports such as AT and CCID can be wrapped directly:

```go
package main

import (
	"context"
	"log"

	"github.com/damonto/uicc-go/at"
	"github.com/damonto/uicc-go/usim"
)

func main() {
	ctx := context.Background()

	tx, err := at.Open("/dev/ttyUSB2", 115200)
	if err != nil {
		log.Fatal(err)
	}

	reader, err := usim.NewReader(tx)
	if err != nil {
		log.Fatal(err)
	}

	card, err := usim.New(ctx, reader)
	if err != nil {
		log.Fatal(err)
	}
	defer card.Close()

	log.Printf("ICCID=%s IMSI=%s MCC=%s MNC=%s", card.ICCID(), card.IMSI(), card.MCC(), card.MNC())
}
```

Pass a logger when the caller owns logging:

```go
card, err := usim.New(ctx, reader, logger)
```

QMI UIM can be adapted with `usim.NewQCOM`:

```go
transport, err := qmi.Open(ctx, qmi.WithProxy("/dev/cdc-wdm0"))
if err != nil {
	return err
}
uimReader, err := uim.New(ctx, transport, uim.WithSlot(1))
if err != nil {
	return err
}
reader, err := usim.NewQCOM(uimReader)
```

MBIM can be adapted with `usim.NewMBIM`:

```go
mbimReader, err := mbim.Open(ctx, mbim.WithProxy("/dev/cdc-wdm0"), mbim.WithSlot(1))
if err != nil {
	return err
}
reader, err := usim.NewMBIM(mbimReader)
```

Once loaded, `*usim.Card` exposes identity and authentication helpers:

```go
result, err := card.AKA(ctx, rand, autn)
result, err := card.EAPAKA(ctx, rand, autn)
```

## Testing

Run all tests:

```sh
GOCACHE=/tmp/uicc-go-build go test ./...
```

Race-test the protocol packages:

```sh
GOCACHE=/tmp/uicc-go-build go test -race ./at ./mbim ./qcom ./qcom/qmi ./qcom/qrtr ./qcom/uim
```

Cross-compile the AT package:

```sh
GOOS=windows GOCACHE=/tmp/uicc-go-build go test -c -o /tmp/uicc-go-at-windows.test.exe ./at
GOOS=darwin GOCACHE=/tmp/uicc-go-build go test -c -o /tmp/uicc-go-at-darwin.test ./at
```

Check module tidiness:

```sh
GOCACHE=/tmp/uicc-go-build go mod tidy -diff
```

## Design Notes

- Protocol readers expose transport and protocol primitives. They should not depend on `usim`.
- `usim` provides card-level adaptation and business behavior on top of readers.
- QMI and MBIM require explicit proxy or direct mode selection.
- QRTR is a top-level QCOM transport package.
- Types implement Go standard interfaces such as `encoding.BinaryMarshaler`, `encoding.BinaryUnmarshaler`, `io.ReaderFrom`, and `io.WriterTo` where those interfaces naturally fit the wire format.

## License

No license file is currently included.
