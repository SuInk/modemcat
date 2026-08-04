package backend

import "github.com/SuInk/djsmsforward/pkg/mbim"

func (b *MBIMBackend) Capability() *mbim.Capabilities {
	return b.source.Capability()
}
