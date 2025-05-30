package name

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	cmdenv "github.com/bittorrent/go-btfs/core/commands/cmdenv"
	namesys "github.com/bittorrent/go-btfs/namesys"

	cmds "github.com/bittorrent/go-btfs-cmds"
	options "github.com/bittorrent/interface-go-btfs-core/options"
	nsopts "github.com/bittorrent/interface-go-btfs-core/options/namesys"
	logging "github.com/ipfs/go-log"
	path "github.com/ipfs/go-path"
)

var log = logging.Logger("core/commands/btns")

type ResolvedPath struct {
	Path path.Path
}

const (
	recursiveOptionName      = "recursive"
	nocacheOptionName        = "nocache"
	dhtRecordCountOptionName = "dht-record-count"
	dhtTimeoutOptionName     = "dht-timeout"
	streamOptionName         = "stream"
)

var IpnsCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline: "Resolve BTNS names.",
		ShortDescription: `
BTNS is a PKI namespace, where names are the hashes of public keys, and
the private key enables publishing new (signed) values. In both publish
and resolve, the default name used is the node's own PeerID,
which is the hash of its public key.
`,
		LongDescription: `
BTNS is a PKI namespace, where names are the hashes of public keys, and
the private key enables publishing new (signed) values. In both publish
and resolve, the default name used is the node's own PeerID,
which is the hash of its public key.

You can use the 'btfs key' commands to list and generate more names and their
respective keys.

Examples:

Resolve the value of your name:

  > btfs name resolve
  /btfs/QmatmE9msSfkKxoffpHwNLNKgwZG8eT9Bud6YoPab52vpy

Resolve the value of another name:

  > btfs name resolve QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ
  /btfs/QmSiTko9JZyabH56y2fussEt1A5oDqsFXB3CkvAqraFryz

Resolve the value of a dnslink:

  > btfs name resolve btfs.io
  /btfs/QmaBvfZooxWkrv7D3r8LS9moNjzD2o525XMZze69hhoxf5

`,
	},

	Arguments: []cmds.Argument{
		cmds.StringArg("name", false, false, "The BTNS name to resolve. Defaults to your node's peerID."),
	},
	Options: []cmds.Option{
		cmds.BoolOption(recursiveOptionName, "r", "Resolve until the result is not an BTNS name.").WithDefault(true),
		cmds.BoolOption(nocacheOptionName, "n", "Do not use cached entries."),
		cmds.UintOption(dhtRecordCountOptionName, "dhtrc", "Number of records to request for DHT resolution."),
		cmds.StringOption(dhtTimeoutOptionName, "dhtt", "Max time to collect values during DHT resolution eg \"30s\". Pass 0 for no timeout."),
		cmds.BoolOption(streamOptionName, "s", "Stream entries as they are found."),
	},
	Run: func(req *cmds.Request, res cmds.ResponseEmitter, env cmds.Environment) error {
		api, err := cmdenv.GetApi(env, req)
		if err != nil {
			return err
		}

		nocache, _ := req.Options["nocache"].(bool)

		var name string
		if len(req.Arguments) == 0 {
			self, err := api.Key().Self(req.Context)
			if err != nil {
				return err
			}
			name = self.ID().String()
		} else {
			name = req.Arguments[0]
		}

		recursive, _ := req.Options[recursiveOptionName].(bool)
		rc, rcok := req.Options[dhtRecordCountOptionName].(uint)
		dhtt, dhttok := req.Options[dhtTimeoutOptionName].(string)
		stream, _ := req.Options[streamOptionName].(bool)

		opts := []options.NameResolveOption{
			options.Name.Cache(!nocache),
		}

		if !recursive {
			opts = append(opts, options.Name.ResolveOption(nsopts.Depth(1)))
		}
		if rcok {
			opts = append(opts, options.Name.ResolveOption(nsopts.DhtRecordCount(rc)))
		}
		if dhttok {
			d, err := time.ParseDuration(dhtt)
			if err != nil {
				return err
			}
			if d < 0 {
				return errors.New("DHT timeout value must be >= 0")
			}
			opts = append(opts, options.Name.ResolveOption(nsopts.DhtTimeout(d)))
		}

		if !strings.HasPrefix(name, "/btns/") {
			name = "/btns/" + name
		}

		if !stream {
			output, err := api.Name().Resolve(req.Context, name, opts...)
			if err != nil && (recursive || err != namesys.ErrResolveRecursion) {
				return err
			}

			return cmds.EmitOnce(res, &ResolvedPath{path.FromString(output.String())})
		}

		output, err := api.Name().Search(req.Context, name, opts...)
		if err != nil {
			return err
		}

		for v := range output {
			if v.Err != nil && (recursive || v.Err != namesys.ErrResolveRecursion) {
				return v.Err
			}
			if err := res.Emit(&ResolvedPath{path.FromString(v.Path.String())}); err != nil {
				return err
			}

		}

		return nil
	},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, rp *ResolvedPath) error {
			_, err := fmt.Fprintln(w, rp.Path)
			return err
		}),
	},
	Type: ResolvedPath{},
}
