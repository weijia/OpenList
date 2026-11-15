package remotestorage

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	Endpoint string `json:"endpoint" required:"true" help:"storage root url including the user, e.g. https://storage.example.com/alice"`
	Token    string `json:"token" required:"true" help:"OAuth bearer token, sent as Authorization: Bearer <token>"`
	driver.RootPath
	TlsInsecureSkipVerify bool `json:"tls_insecure_skip_verify" default:"false" help:"skip TLS certificate verification"`
}

var config = driver.Config{
	Name:        "RemoteStorage",
	LocalSort:   true,
	DefaultRoot: "/",
	// Documents are only readable with an Authorization header, so downloads
	// must always go through OpenList's proxy instead of a bare direct link.
	OnlyProxy: true,
	NoCache:   false,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &RemoteStorage{}
	})
}
