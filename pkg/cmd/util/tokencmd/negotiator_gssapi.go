// +build gssapi

package tokencmd

import (
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/apcera/gssapi"
	"github.com/golang/glog"

	utilerrors "k8s.io/kubernetes/pkg/util/errors"
)

func GSSAPIEnabled() bool {
	return true
}

type gssapiNegotiator struct {
	// handle to a loaded gssapi lib
	lib *gssapi.Lib
	// error seen when loading the lib
	loadError error
	// lock to make sure we only load it once
	loadOnce sync.Once

	// service name of the server we are negotiating against
	name *gssapi.Name
	// security context holding the state of the negotiation
	ctx *gssapi.CtxId
	// flags indicating what options we want for the negotiation
	// TODO: surface option for mutual authentication, e.g. gssapi.GSS_C_MUTUAL_FLAG
	flags uint32

	// track whether the last response from InitSecContext was GSS_S_COMPLETE
	complete bool
}

func NewGSSAPINegotiator() Negotiater {
	return &gssapiNegotiator{}
}

func (g *gssapiNegotiator) InitSecContext(requestURL string, challengeToken []byte) (tokenToSend []byte, err error) {
	lib, err := g.loadLib()
	if err != nil {
		return nil, err
	}

	// Initialize our context if we haven't already
	if g.ctx == nil {
		u, err := url.Parse(requestURL)
		if err != nil {
			return nil, err
		}

		hostname := u.Host
		if h, _, err := net.SplitHostPort(u.Host); err == nil {
			hostname = h
		}

		serviceName := "HTTP@" + hostname
		glog.V(5).Infof("importing service name %s", serviceName)
		nameBuf, err := lib.MakeBufferString(serviceName)
		if err != nil {
			return nil, err
		}
		defer nameBuf.Release()

		name, err := nameBuf.Name(lib.GSS_C_NT_HOSTBASED_SERVICE)
		if err != nil {
			return nil, err
		}
		g.name = name
		g.ctx = lib.GSS_C_NO_CONTEXT
	}

	incomingTokenBuffer, err := lib.MakeBufferBytes(challengeToken)
	if err != nil {
		return nil, err
	}
	defer incomingTokenBuffer.Release()

	var outgoingToken *gssapi.Buffer
	g.ctx, _, outgoingToken, _, _, err = lib.InitSecContext(lib.GSS_C_NO_CREDENTIAL, g.ctx, g.name, lib.GSS_C_NO_OID, g.flags, time.Duration(0), lib.GSS_C_NO_CHANNEL_BINDINGS, incomingTokenBuffer)
	defer outgoingToken.Release()

	switch err {
	case nil:
		glog.V(5).Infof("InitSecContext returned GSS_S_COMPLETE")
		g.complete = true
		return outgoingToken.Bytes(), nil
	case gssapi.ErrContinueNeeded:
		glog.V(5).Infof("InitSecContext returned GSS_S_CONTINUE_NEEDED")
		g.complete = false
		return outgoingToken.Bytes(), nil
	default:
		glog.V(5).Infof("InitSecContext returned error: %v", err)
		return nil, err
	}
}

func (g *gssapiNegotiator) IsComplete() bool {
	return g.complete
}

func (g *gssapiNegotiator) Release() error {
	var errs []error
	if err := g.name.Release(); err != nil {
		errs = append(errs, err)
	}
	if err := g.ctx.Release(); err != nil {
		errs = append(errs, err)
	}
	if err := g.lib.Unload(); err != nil {
		errs = append(errs, err)
	}
	return utilerrors.NewAggregate(errs)
}

func (g *gssapiNegotiator) loadLib() (*gssapi.Lib, error) {
	g.loadOnce.Do(func() {
		glog.V(5).Infof("loading gssapi")
		g.lib, g.loadError = gssapi.Load(nil)
		if g.loadError != nil {
			glog.V(5).Infof("could not load gssapi: %v", g.loadError)
		}
	})
	return g.lib, g.loadError
}
