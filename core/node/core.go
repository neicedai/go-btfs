package node

import (
	"context"
	"fmt"

	"github.com/bittorrent/go-btfs/core/node/helpers"
	"github.com/bittorrent/go-btfs/repo"
	irouting "github.com/bittorrent/go-btfs/routing"
	"github.com/bittorrent/go-mfs"
	"github.com/bittorrent/go-unixfs"
	"github.com/ipfs/boxo/bitswap"
	"github.com/ipfs/boxo/bitswap/network"
	"github.com/ipfs/go-blockservice"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-fetcher"
	bsfetcher "github.com/ipfs/go-fetcher/impl/blockservice"
	"github.com/ipfs/go-filestore"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	exchange "github.com/ipfs/go-ipfs-exchange-interface"
	pin "github.com/ipfs/go-ipfs-pinner"
	"github.com/ipfs/go-ipfs-pinner/dspinner"
	format "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	"github.com/ipfs/go-unixfsnode"
	dagpb "github.com/ipld/go-codec-dagpb"
	"github.com/ipld/go-ipld-prime"
	"github.com/ipld/go-ipld-prime/node/basicnode"
	"github.com/ipld/go-ipld-prime/schema"
	"github.com/libp2p/go-libp2p/core/host"
	"go.uber.org/fx"
)

// BlockService creates new blockservice which provides an interface to fetch content-addressable blocks
func BlockService(lc fx.Lifecycle, bs blockstore.Blockstore, rem exchange.Interface) blockservice.BlockService {
	bsvc := blockservice.New(bs, rem)

	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			return bsvc.Close()
		},
	})

	return bsvc
}

// Pinning creates new pinner which tells GC which blocks should be kept
func Pinning(bstore blockstore.Blockstore, ds format.DAGService, repo repo.Repo) (pin.Pinner, error) {
	// internalDag := merkledag.NewDAGService(blockservice.New(bstore, offline.Exchange(bstore)))
	rootDS := repo.Datastore()
	// ctx := context.Background()
	syncFn := func(ctx context.Context) error {
		if err := rootDS.Sync(ctx, blockstore.BlockPrefix); err != nil {
			return err
		}
		return rootDS.Sync(ctx, filestore.FilestorePrefix)
	}
	syncDs := &syncDagService{ds, syncFn}

	ctx := context.TODO()

	pinning, err := dspinner.New(ctx, rootDS, syncDs)
	if err != nil {
		return nil, err
	}

	return pinning, nil
}

var (
	_ merkledag.SessionMaker = new(syncDagService)
	_ format.DAGService      = new(syncDagService)
)

// syncDagService is used by the Pinner to ensure data gets persisted to the underlying datastore
type syncDagService struct {
	format.DAGService
	syncFn func(ctx context.Context) error
}

func (s *syncDagService) Sync(ctx context.Context) error {
	return s.syncFn(ctx)
}

func (s *syncDagService) Session(ctx context.Context) format.NodeGetter {
	return merkledag.NewSession(ctx, s.DAGService)
}

type fetchersOut struct {
	fx.Out
	IPLDFetcher   fetcher.Factory `name:"ipldFetcher"`
	UnixfsFetcher fetcher.Factory `name:"unixfsFetcher"`
}

// FetcherConfig returns a fetcher config that can build new fetcher instances
func FetcherConfig(bs blockservice.BlockService) fetchersOut {
	ipldFetcher := bsfetcher.NewFetcherConfig(bs)
	ipldFetcher.PrototypeChooser = dagpb.AddSupportToChooser(func(lnk ipld.Link, lnkCtx ipld.LinkContext) (ipld.NodePrototype, error) {
		if tlnkNd, ok := lnkCtx.LinkNode.(schema.TypedLinkNode); ok {
			return tlnkNd.LinkTargetNodePrototype(), nil
		}
		return basicnode.Prototype.Any, nil
	})

	unixFSFetcher := ipldFetcher.WithReifier(unixfsnode.Reify)
	return fetchersOut{IPLDFetcher: ipldFetcher, UnixfsFetcher: unixFSFetcher}
}

// Dag creates new DAGService
func Dag(bs blockservice.BlockService) format.DAGService {
	return merkledag.NewDAGService(bs)
}

// OnlineExchange creates new LibP2P backed block exchange (BitSwap)
func OnlineExchange(provide bool) interface{} {
	return func(mctx helpers.MetricsCtx, lc fx.Lifecycle, host host.Host, rt irouting.ProvideManyRouter, bs blockstore.GCBlockstore) exchange.Interface {
		bitswapNetwork := network.NewFromIpfsHost(host, rt)
		exch := bitswap.New(helpers.LifecycleCtx(mctx, lc), bitswapNetwork, bs, bitswap.ProvideEnabled(provide))
		lc.Append(fx.Hook{
			OnStop: func(ctx context.Context) error {
				return exch.Close()
			},
		})
		return exch

	}
}

// Files loads persisted MFS root
func Files(mctx helpers.MetricsCtx, lc fx.Lifecycle, repo repo.Repo, dag format.DAGService) (*mfs.Root, error) {
	dsk := datastore.NewKey("/local/filesroot")
	pf := func(ctx context.Context, c cid.Cid) error {
		rootDS := repo.Datastore()
		if err := rootDS.Sync(ctx, blockstore.BlockPrefix); err != nil {
			return err
		}
		if err := rootDS.Sync(ctx, filestore.FilestorePrefix); err != nil {
			return err
		}

		if err := rootDS.Put(ctx, dsk, c.Bytes()); err != nil {
			return err
		}
		return rootDS.Sync(ctx, dsk)
	}

	var nd *merkledag.ProtoNode
	val, err := repo.Datastore().Get(mctx, dsk)
	ctx := helpers.LifecycleCtx(mctx, lc)

	switch {
	case err == datastore.ErrNotFound || val == nil:
		nd = unixfs.EmptyDirNode()
		err := dag.Add(ctx, nd)
		if err != nil {
			return nil, fmt.Errorf("failure writing to dagstore: %s", err)
		}
	case err == nil:
		c, err := cid.Cast(val)
		if err != nil {
			return nil, err
		}

		rnd, err := dag.Get(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("error loading filesroot from DAG: %s", err)
		}

		pbnd, ok := rnd.(*merkledag.ProtoNode)
		if !ok {
			return nil, merkledag.ErrNotProtobuf
		}

		nd = pbnd
	default:
		return nil, err
	}

	root, err := mfs.NewRoot(ctx, dag, nd, pf)

	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			return root.Close()
		},
	})

	return root, err
}
