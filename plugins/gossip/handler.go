package gossip

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cryptix/go/logging"
	"github.com/go-kit/kit/metrics/prometheus"
	"github.com/pkg/errors"
	"go.cryptoscope.co/librarian"
	"go.cryptoscope.co/margaret"
	"go.cryptoscope.co/margaret/multilog"
	"go.cryptoscope.co/muxrpc"
	"go.cryptoscope.co/ssb"
	"go.cryptoscope.co/ssb/graph"
)

type handler struct {
	Id           *ssb.FeedRef
	RootLog      margaret.Log
	UserFeeds    multilog.MultiLog
	GraphBuilder graph.Builder
	Info         logging.Interface

	hopCount int
	promisc  bool // ask for remote feed even if it's not on owns fetch list

	activeLock  sync.Mutex
	activeFetch sync.Map
	// TOOD: sync.Map is useless here
	// use normal map activeFetch map[[32]byte]struct{}

	// this map caches the graph auth lookup tree for multiple calls
	distLookupLock sync.Mutex
	distLookup     map[[32]byte]*graph.Lookup

	hanlderDone func()

	sysGauge *prometheus.Gauge
	sysCtr   *prometheus.Counter
}

func (g *handler) HandleConnect(ctx context.Context, e muxrpc.Endpoint) {
	defer func() {
		// TODO: just used for testing...
		// maybe make an interface wrapper instead
		g.hanlderDone()
	}()

	hasOwn, err := multilog.Has(g.UserFeeds, librarian.Addr(g.Id.ID))
	if err != nil {
		g.Info.Log("handleConnect", "multilog.Has(g.UserFeeds,myID)", "err", err)
		return
	}

	if !hasOwn {
		g.Info.Log("handleConnect", "oops - dont have my own feed. requesting")
		if err := g.fetchFeed(ctx, g.Id, e); err != nil {
			g.Info.Log("handleConnect", "my fetchFeed failed", "r", g.Id.Ref(), "err", err)
			return
		}
		g.Info.Log("fetchFeed", "done self")
	}

	g.distLookupLock.Lock()
	remoteRef, err := ssb.GetFeedRefFromAddr(e.Remote())
	if err != nil {
		g.Info.Log("handleConnect", "no remote ref", "err", err)
		g.distLookupLock.Unlock()
		return
	}

	if g.promisc {
		hasCallee, err := multilog.Has(g.UserFeeds, librarian.Addr(remoteRef.ID))
		if err != nil {
			g.Info.Log("handleConnect", "multilog.Has(callee)", "ref", remoteRef.Ref(), "err", err)
			g.distLookupLock.Unlock()
			return
		}

		if !hasCallee {
			g.Info.Log("handleConnect", "oops - dont have calling feed. requesting")
			if err := g.fetchFeed(ctx, remoteRef, e); err != nil {
				g.Info.Log("handleConnect", "fetchFeed callee failed", "ref", remoteRef.Ref(), "err", err)
				g.distLookupLock.Unlock()
				return
			}
			g.Info.Log("fetchFeed", "done callee", "ref", remoteRef.Ref())
		}
	}

	// TODO: ctx to build and list?!
	// or pass rootCtx to their constructor but than we can't cancel sessions
	select {
	case <-ctx.Done():
		g.distLookupLock.Unlock()
		return
	default:
	}

	tGraph, err := g.GraphBuilder.Build()
	if err != nil {
		g.Info.Log("handleConnect", "fetchFeed hops listing", "err", err)
		g.distLookupLock.Unlock()
		return
	}

	lookup, err := tGraph.MakeDijkstra(remoteRef)
	if err != nil {
		g.Info.Log("handleConnect", "fetchFeed hops listing", "err", err)
		g.distLookupLock.Unlock()
		return
	}

	var k [32]byte
	copy(k[:], remoteRef.ID)
	g.distLookup[k] = lookup
	g.distLookupLock.Unlock()

	ufaddrs, err := g.UserFeeds.List()
	if err != nil {
		g.Info.Log("handleConnect", "UserFeeds listing failed", "err", err)
		return
	}

	var blockedAddr []librarian.Addr
	blocked := tGraph.BlockedList(g.Id)
	for _, ref := range ufaddrs {
		var k [32]byte
		copy(k[:], []byte(ref))
		if _, isBlocked := blocked[k]; isBlocked {
			blockedAddr = append(blockedAddr, ref)
		}
	}

	hops := g.GraphBuilder.Hops(g.Id, g.hopCount)
	if hops != nil {
		g.fetchAllMinus(ctx, e, hops, append(ufaddrs, blockedAddr...))
	}

	err = g.fetchAllLib(ctx, e, ufaddrs)
	if muxrpc.IsSinkClosed(err) || errors.Cause(err) == context.Canceled {
		return
	}

	g.Info.Log("msg", "fetchHops done", "n", hops.Count())
	<-ctx.Done()
}

func (g *handler) check(err error) {
	if err != nil {
		g.Info.Log("error", err)
	}
}

func (g *handler) HandleCall(ctx context.Context, req *muxrpc.Request, edp muxrpc.Endpoint) {
	if req.Type == "" {
		req.Type = "async"
	}

	var closed bool
	checkAndClose := func(err error) {
		g.check(err)
		if err != nil {
			closed = true
			closeErr := req.Stream.CloseWithError(err)
			g.check(errors.Wrapf(closeErr, "error closeing request. %s", req.Method))
		}
	}

	defer func() {
		if !closed {
			g.check(errors.Wrapf(req.Stream.Close(), "gossip: error closing call: %s", req.Method))
		}
	}()

	switch req.Method.String() {

	case "createHistoryStream":
		if req.Type != "source" {
			checkAndClose(errors.Errorf("createHistoryStream: wrong tipe. %s", req.Type))
			return
		}

		g.distLookupLock.Lock()
		remoteRef, err := ssb.GetFeedRefFromAddr(edp.Remote())
		if err != nil {
			checkAndClose(err)
			g.distLookupLock.Lock()
			return
		}

		var k [32]byte
		copy(k[:], remoteRef.ID)
		lookup, ok := g.distLookup[k]
		if !ok {
			checkAndClose(fmt.Errorf("no lookup for remote"))
			g.distLookupLock.Lock()
			return
		}
		g.distLookupLock.Unlock()

		if err := g.pourFeed(ctx, req, lookup); err != nil {
			checkAndClose(errors.Wrap(err, "createHistoryStream failed"))
			return
		}
		return

	case "gossip.ping":
		if err := g.ping(ctx, req); err != nil {
			checkAndClose(errors.Wrap(err, "gossip.ping failed."))
			return
		}

	default:
		checkAndClose(errors.Errorf("unknown command: %s", req.Method))
	}
}

func (g *handler) ping(ctx context.Context, req *muxrpc.Request) error {
	err := req.Stream.Pour(ctx, time.Now().UnixNano()/1000000)
	if err != nil {
		return errors.Wrapf(err, "pour failed to pong")
	}
	// just leave it open..
	return nil
}
