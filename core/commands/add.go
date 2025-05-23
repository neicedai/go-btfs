package commands

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/bittorrent/go-btfs/chain/abi"
	chainconfig "github.com/bittorrent/go-btfs/chain/config"
	oldcmds "github.com/bittorrent/go-btfs/commands"
	"github.com/bittorrent/go-btfs/core/commands/cmdenv"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	ethCrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	cmds "github.com/bittorrent/go-btfs-cmds"
	files "github.com/bittorrent/go-btfs-files"
	coreiface "github.com/bittorrent/interface-go-btfs-core"
	"github.com/bittorrent/interface-go-btfs-core/options"
	coreifacePath "github.com/bittorrent/interface-go-btfs-core/path"
	mh "github.com/multiformats/go-multihash"
	pb "gopkg.in/cheggaaa/pb.v1"
)

// ErrDepthLimitExceeded indicates that the max depth has been exceeded.
var ErrDepthLimitExceeded = fmt.Errorf("depth limit exceeded")

type TimeParts struct {
	t *time.Time
}

func (t TimeParts) MarshalJSON() ([]byte, error) {
	return t.t.MarshalJSON()
}

// UnmarshalJSON implements the json.Unmarshaler interface.
// The time is expected to be a quoted string in RFC 3339 format.
func (t *TimeParts) UnmarshalJSON(data []byte) (err error) {
	// Fractional seconds are handled implicitly by Parse.
	tt, err := time.Parse("\"2006-01-02T15:04:05Z\"", string(data))
	*t = TimeParts{&tt}
	return
}

type AddEvent struct {
	Name  string
	Hash  string `json:",omitempty"`
	Bytes int64  `json:",omitempty"`
	Size  string `json:",omitempty"`
	Mode  string `json:",omitempty"`
	Mtime int64  `json:",omitempty"`
}

const (
	quietOptionName              = "quiet"
	quieterOptionName            = "quieter"
	silentOptionName             = "silent"
	progressOptionName           = "progress"
	trickleOptionName            = "trickle"
	wrapOptionName               = "wrap-with-directory"
	onlyHashOptionName           = "only-hash"
	chunkerOptionName            = "chunker"
	pinOptionName                = "pin"
	rawLeavesOptionName          = "raw-leaves"
	noCopyOptionName             = "nocopy"
	fstoreCacheOptionName        = "fscache"
	hashOptionName               = "hash"
	inlineOptionName             = "inline"
	inlineLimitOptionName        = "inline-limit"
	tokenMetaOptionName          = "meta"
	encryptName                  = "encrypt"
	pubkeyName                   = "public-key"
	peerIdName                   = "peer-id"
	pinDurationCountOptionName   = "pin-duration-count"
	uploadToBlockchainOptionName = "to-blockchain"
	preserveModeOptionName       = "preserve-mode"
	preserveMtimeOptionName      = "preserve-mtime"
	modeOptionName               = "mode"
	mtimeOptionName              = "mtime"
)

const adderOutChanSize = 8

var AddCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline: "Add a file or directory to btfs.",
		ShortDescription: `
Adds contents of <path> to btfs. Use -r to add directories (recursively).
`,
		LongDescription: `
Adds contents of <path> to btfs. Use -r to add directories.
Note that directories are added recursively, to form the btfs
MerkleDAG.

If the daemon is not running, it will just add locally.
If the daemon is started later, it will be advertised after a few
seconds when the reprovider runs.

The wrap option, '-w', wraps the file (or files, if using the
recursive option) in a directory. This directory contains only
the files which have been added, and means that the file retains
its filename. For example:

  > btfs add example.jpg
  added QmbFMke1KXqnYyBBWxB74N4c5SBnJMVAiMNRcGu6x1AwQH example.jpg
  > btfs add example.jpg -w
  added QmbFMke1KXqnYyBBWxB74N4c5SBnJMVAiMNRcGu6x1AwQH example.jpg
  added QmaG4FuMqEBnQNn3C8XJ5bpW8kLs7zq2ZXgHptJHbKDDVx

You can now refer to the added file in a gateway, like so:

  /btfs/QmaG4FuMqEBnQNn3C8XJ5bpW8kLs7zq2ZXgHptJHbKDDVx/example.jpg

The chunker option, '-s', specifies the chunking strategy that dictates
how to break files into blocks. Blocks with same content can
be deduplicated. Different chunking strategies will produce different
hashes for the same file. The default is a fixed block size of
256 * 1024 bytes, 'size-262144'. Alternatively, you can use the
Rabin fingerprint chunker for content defined chunking by specifying
rabin-[min]-[avg]-[max] (where min/avg/max refer to the desired
chunk sizes in bytes), e.g. 'rabin-262144-524288-1048576'.
Buzhash or Rabin fingerprint chunker for content defined chunking by
specifying buzhash or rabin-[min]-[avg]-[max] (where min/avg/max refer
to the desired chunk sizes in bytes), e.g. 'rabin-262144-524288-1048576'.
For replicated files intended for host storage, reed-solomon should be
used with default settings. It is also supported to customize data and
parity shards using reed-solomon-[#data]-[#parity]-[size].

The following examples use very small byte sizes to demonstrate the
properties of the different chunkers on a small file. You'll likely
want to use a 1024 times larger chunk sizes for most files.

  > btfs add --chunker=size-2048 btfs-logo.svg
  added QmafrLBfzRLV4XSH1XcaMMeaXEUhDJjmtDfsYU95TrWG87 btfs-logo.svg
  > btfs add --chunker=rabin-512-1024-2048 btfs-logo.svg
  added Qmf1hDN65tR55Ubh2RN1FPxr69xq3giVBz1KApsresY8Gn btfs-logo.svg

You can now check what blocks have been created by:

  > btfs object links QmafrLBfzRLV4XSH1XcaMMeaXEUhDJjmtDfsYU95TrWG87
  QmY6yj1GsermExDXoosVE3aSPxdMNYr6aKuw3nA8LoWPRS 2059
  Qmf7ZQeSxq2fJVJbCmgTrLLVN9tDR9Wy5k75DxQKuz5Gyt 1195
  > btfs object links Qmf1hDN65tR55Ubh2RN1FPxr69xq3giVBz1KApsresY8Gn
  QmY6yj1GsermExDXoosVE3aSPxdMNYr6aKuw3nA8LoWPRS 2059
  QmerURi9k4XzKCaaPbsK6BL5pMEjF7PGphjDvkkjDtsVf3 868
  QmQB28iwSriSUSMqG2nXDTLtdPHgWb4rebBrU7Q1j4vxPv 338

Finally, a note on hash determinism. While not guaranteed, adding the same
file/directory with the same flags will almost always result in the same output
hash. However, almost all of the flags provided by this command (other than pin,
only-hash, and progress/status related flags) will change the final hash.
`,
	},

	Arguments: []cmds.Argument{
		cmds.FileArg("path", true, true, "The path to a file to be added to btfs.").EnableRecursive().EnableStdin(),
	},
	Options: []cmds.Option{
		cmds.OptionRecursivePath, // a builtin option that allows recursive paths (-r, --recursive)
		cmds.OptionDerefArgs,     // a builtin option that resolves passed in filesystem links (--dereference-args)
		cmds.OptionStdinName,     // a builtin option that optionally allows wrapping stdin into a named file
		cmds.OptionHidden,
		cmds.OptionIgnore,
		cmds.OptionIgnoreRules,
		cmds.BoolOption(quietOptionName, "q", "Write minimal output."),
		cmds.BoolOption(quieterOptionName, "Q", "Write only final hash."),
		cmds.BoolOption(silentOptionName, "Write no output."),
		cmds.BoolOption(progressOptionName, "p", "Stream progress data."),
		cmds.BoolOption(trickleOptionName, "t", "Use trickle-dag format for dag generation."),
		cmds.BoolOption(onlyHashOptionName, "n", "Only chunk and hash - do not write to disk."),
		cmds.BoolOption(wrapOptionName, "w", "Wrap files with a directory object."),
		cmds.StringOption(chunkerOptionName, "s", "Chunking algorithm, size-[bytes], rabin-[min]-[avg]-[max], buzhash or reed-solomon-[#data]-[#parity]-[size]").WithDefault("size-262144"),
		cmds.BoolOption(pinOptionName, "Pin this object when adding.").WithDefault(true),
		cmds.BoolOption(rawLeavesOptionName, "Use raw blocks for leaf nodes. (experimental)"),
		cmds.BoolOption(noCopyOptionName, "Add the file using filestore. Implies raw-leaves. (experimental)"),
		cmds.BoolOption(fstoreCacheOptionName, "Check the filestore for pre-existing blocks. (experimental)"),
		cmds.IntOption(cidVersionOptionName, "CID version. Defaults to 0 unless an option that depends on CIDv1 is passed. (experimental)"),
		cmds.StringOption(hashOptionName, "Hash function to use. Implies CIDv1 if not sha2-256. (experimental)").WithDefault("sha2-256"),
		cmds.BoolOption(inlineOptionName, "Inline small blocks into CIDs. (experimental)"),
		cmds.IntOption(inlineLimitOptionName, "Maximum block size to inline. (experimental)").WithDefault(32),
		cmds.StringOption(tokenMetaOptionName, "m", "Token metadata in JSON string"),
		cmds.BoolOption(encryptName, "Encrypt the file."),
		cmds.StringOption(pubkeyName, "The public key to encrypt the file."),
		cmds.StringOption(peerIdName, "The peer id to encrypt the file."),
		cmds.IntOption(pinDurationCountOptionName, "d", "Duration for which the object is pinned in days.").WithDefault(0),
		cmds.BoolOption(uploadToBlockchainOptionName, "add file meta to blockchain").WithDefault(false),
		cmds.BoolOption(preserveModeOptionName, "Apply existing POSIX permissions to created UnixFS entries. Disables raw-leaves. (experimental)"),
		cmds.BoolOption(preserveMtimeOptionName, "Apply existing POSIX modification time to created UnixFS entries. Disables raw-leaves. (experimental)"),
		cmds.UintOption(modeOptionName, "Custom POSIX file mode to store in created UnixFS entries. Disables raw-leaves. (experimental)"),
		cmds.Int64Option(mtimeOptionName, "Custom POSIX modification time to store in created UnixFS entries (seconds before or after the Unix Epoch). Disables raw-leaves. (experimental)"),
	},
	PreRun: func(req *cmds.Request, env cmds.Environment) error {
		quiet, _ := req.Options[quietOptionName].(bool)
		quieter, _ := req.Options[quieterOptionName].(bool)
		quiet = quiet || quieter

		silent, _ := req.Options[silentOptionName].(bool)

		if quiet || silent {
			return nil
		}

		// btfs cli progress bar defaults to true unless quiet or silent is used
		_, found := req.Options[progressOptionName].(bool)
		if !found {
			req.Options[progressOptionName] = true
		}

		return nil
	},
	Run: func(req *cmds.Request, res cmds.ResponseEmitter, env cmds.Environment) error {
		api, err := cmdenv.GetApi(env, req)
		if err != nil {
			return err
		}

		progress, _ := req.Options[progressOptionName].(bool)
		trickle, _ := req.Options[trickleOptionName].(bool)
		wrap, _ := req.Options[wrapOptionName].(bool)
		hash, _ := req.Options[onlyHashOptionName].(bool)
		silent, _ := req.Options[silentOptionName].(bool)
		chunker, _ := req.Options[chunkerOptionName].(string)
		dopin, _ := req.Options[pinOptionName].(bool)
		rawblks, rbset := req.Options[rawLeavesOptionName].(bool)
		nocopy, _ := req.Options[noCopyOptionName].(bool)
		fscache, _ := req.Options[fstoreCacheOptionName].(bool)
		cidVer, cidVerSet := req.Options[cidVersionOptionName].(int)
		hashFunStr, _ := req.Options[hashOptionName].(string)
		inline, _ := req.Options[inlineOptionName].(bool)
		inlineLimit, _ := req.Options[inlineLimitOptionName].(int)
		tokenMetadata, _ := req.Options[tokenMetaOptionName].(string)
		encrypt, _ := req.Options[encryptName].(bool)
		pubkey, _ := req.Options[pubkeyName].(string)
		peerId, _ := req.Options[peerIdName].(string)
		pinDuration, _ := req.Options[pinDurationCountOptionName].(int)
		uploadToBlockchain, _ := req.Options[uploadToBlockchainOptionName].(bool)
		preserveMode, _ := req.Options[preserveModeOptionName].(bool)
		preserveMtime, _ := req.Options[preserveMtimeOptionName].(bool)
		mode, _ := req.Options[modeOptionName].(uint)
		mtime, _ := req.Options[mtimeOptionName].(int64)

		hashFunCode, ok := mh.Names[strings.ToLower(hashFunStr)]
		if !ok {
			return fmt.Errorf("unrecognized hash function: %s", strings.ToLower(hashFunStr))
		}

		enc, err := cmdenv.GetCidEncoder(req)
		if err != nil {
			return err
		}

		toadd := req.Files
		if wrap {
			toadd = files.NewSliceDirectory([]files.DirEntry{
				files.FileEntry("", req.Files),
			})
		}

		opts := []options.UnixfsAddOption{
			options.Unixfs.Hash(hashFunCode),

			options.Unixfs.Inline(inline),
			options.Unixfs.InlineLimit(inlineLimit),

			options.Unixfs.Chunker(chunker),

			options.Unixfs.Pin(dopin),
			options.Unixfs.HashOnly(hash),
			options.Unixfs.FsCache(fscache),
			options.Unixfs.Nocopy(nocopy),

			options.Unixfs.Progress(progress),
			options.Unixfs.Silent(silent),

			options.Unixfs.TokenMetadata(tokenMetadata),
			options.Unixfs.PinDuration(int64(pinDuration)),

			options.Unixfs.PreserveMode(preserveMode),
			options.Unixfs.PreserveMtime(preserveMtime),
		}

		if cidVerSet {
			opts = append(opts, options.Unixfs.CidVersion(cidVer))
		}

		if rbset {
			opts = append(opts, options.Unixfs.RawLeaves(rawblks))
		}

		// Storing optional mode or mtime (UnixFS 1.5) requires root block
		// to always be 'dag-pb' and not 'raw'. Below adjusts raw-leaves setting, if possible.
		if preserveMode || preserveMtime || mode != 0 || mtime != 0 {
			// Error if --raw-leaves flag was explicitly passed by the user.
			// (let user make a decision to manually disable it and retry)
			if rbset && rawblks {
				return fmt.Errorf("%s can't be used with UnixFS metadata like mode or modification time", rawLeavesOptionName)
			}
			// No explicit preference from user, disable raw-leaves and continue
			rbset = true
			rawblks = false
		}

		if trickle {
			opts = append(opts, options.Unixfs.Layout(options.TrickleLayout))
		}

		if encrypt {
			opts = append(opts, options.Unixfs.Encrypt(encrypt))
			opts = append(opts, options.Unixfs.Pubkey(pubkey))
			opts = append(opts, options.Unixfs.PeerId(peerId))
		}

		if mode != 0 {
			opts = append(opts, options.Unixfs.Mode(os.FileMode(mode)))
		}
		if mtime != 0 {
			opts = append(opts, options.Unixfs.Mtime(mtime))
		}

		opts = append(opts, nil) // events option placeholder

		var added int
		addit := toadd.Entries()
		for addit.Next() {
			_, dir := addit.Node().(files.Directory)
			errCh := make(chan error, 1)
			events := make(chan interface{}, adderOutChanSize)
			opts[len(opts)-1] = options.Unixfs.Events(events)
			var pr coreifacePath.Resolved
			go func() {
				var err error
				defer close(events)
				pr, err = api.Unixfs().Add(req.Context, addit.Node(), opts...)
				errCh <- err
			}()

			for event := range events {
				output, ok := event.(*coreiface.AddEvent)
				if !ok {
					return errors.New("unknown event type")
				}

				h := ""
				if output.Path != nil {
					h = enc.Encode(output.Path.Cid())
				}

				if !dir && addit.Name() != "" {
					output.Name = addit.Name()
				} else {
					output.Name = path.Join(addit.Name(), output.Name)
				}

				addEvent := AddEvent{
					Name:  output.Name,
					Hash:  h,
					Bytes: output.Bytes,
					Size:  output.Size,
					Mtime: output.Mtime,
				}

				if output.Mode != 0 {
					addEvent.Mode = "0" + strconv.FormatUint(uint64(output.Mode), 8)
				}

				if err := res.Emit(&addEvent); err != nil {
					return err
				}
			}

			if err := <-errCh; err != nil {
				return err
			}
			added++
			if uploadToBlockchain {
				cctx := env.(*oldcmds.Context)
				cfg, err := cctx.GetConfig()
				if err != nil {
					return err
				}
				fname := addit.Name()
				size, _ := addit.Node().Size()
				cli, err := ethclient.Dial(cfg.ChainInfo.Endpoint)
				if err != nil {
					return err
				}
				defer cli.Close()
				currChainCfg, ok := chainconfig.GetChainConfig(cfg.ChainInfo.ChainId)
				if !ok {
					return fmt.Errorf("chain %d is not supported yet", cfg.ChainInfo.ChainId)
				}
				contractAddress := currChainCfg.FileMetaAddress
				contr, err := abi.NewFileMeta(contractAddress, cli)
				if err != nil {
					return err
				}
				pkbytesOri, err := base64.StdEncoding.DecodeString(cfg.Identity.PrivKey)
				if err != nil {
					return err
				}
				privateKey, err := ethCrypto.ToECDSA(pkbytesOri[4:])
				if err != nil {
					return err
				}
				fromAddress := crypto.PubkeyToAddress(privateKey.PublicKey)
				nonce, err := cli.PendingNonceAt(req.Context, fromAddress)
				if err != nil {
					return err
				}
				auth, err := bind.NewKeyedTransactorWithChainID(privateKey, big.NewInt(cfg.ChainInfo.ChainId))
				if err != nil {
					return err
				}
				auth.Nonce = big.NewInt(int64(nonce))
				auth.Value = big.NewInt(0)
				data := abi.FileMetaFileMetaData{
					OwnerPeerId: cfg.Identity.PeerID,
					From:        common.HexToAddress(cfg.Identity.BttcAddr),
					FileName:    fname,
					FileExt:     path.Ext(fname),
					IsDir:       dir,
					FileSize:    big.NewInt(size),
				}
				tx, err := contr.AddFileMeta(auth, pr.Cid().String(), data)
				if err != nil {
					return err
				}
				fmt.Println("Write into file meta contract successfully! Transaction hash is: ", tx.Hash().Hex())
			}
		}

		if addit.Err() != nil {
			return addit.Err()
		}

		if added == 0 {
			return fmt.Errorf("expected a file argument")
		}

		return nil
	},
	PostRun: cmds.PostRunMap{
		cmds.CLI: func(res cmds.Response, re cmds.ResponseEmitter) error {
			sizeChan := make(chan int64, 1)
			outChan := make(chan interface{})
			req := res.Request()

			// Could be slow.
			go func() {
				op := res.Request().Options[encryptName]
				encrypt := op != nil && op.(bool)
				if encrypt {
					it := req.Files.Entries()
					var size int64 = 0
					for it.Next() {
						s, err := it.Node().Size()
						if err != nil {
							log.Warnf("error getting files size: %s", err)
							// see comment above
							return
						}
						blockCount := s/16 + 1
						size += blockCount * 32
						sizeChan <- size
					}
				} else {
					size, err := req.Files.Size()
					if err != nil {
						log.Warnf("error getting files size: %s", err)
						// see comment above
						return
					}
					sizeChan <- size
				}
			}()

			progressBar := func(wait chan struct{}) {
				defer close(wait)

				quiet, _ := req.Options[quietOptionName].(bool)
				quieter, _ := req.Options[quieterOptionName].(bool)
				quiet = quiet || quieter

				progress, _ := req.Options[progressOptionName].(bool)

				var bar *pb.ProgressBar
				if progress {
					bar = pb.New64(0).SetUnits(pb.U_BYTES)
					bar.ManualUpdate = true
					bar.ShowTimeLeft = false
					bar.ShowPercent = false
					bar.Output = os.Stderr
					bar.Start()
				}

				lastFile := ""
				lastHash := ""
				var totalProgress, prevFiles, lastBytes int64

			LOOP:
				for {
					select {
					case out, ok := <-outChan:
						if !ok {
							if quieter {
								fmt.Fprintln(os.Stdout, lastHash)
							}

							break LOOP
						}
						output := out.(*AddEvent)
						if len(output.Hash) > 0 {
							lastHash = output.Hash
							if quieter {
								continue
							}

							if progress {
								// clear progress bar line before we print "added x" output
								fmt.Fprintf(os.Stderr, "\033[2K\r")
							}
							if quiet {
								fmt.Fprintf(os.Stdout, "%s\n", output.Hash)
							} else {
								fmt.Fprintf(os.Stdout, "added %s %s\n", output.Hash, output.Name)
							}

						} else {
							if !progress {
								continue
							}

							if len(lastFile) == 0 {
								lastFile = output.Name
							}
							if output.Name != lastFile || output.Bytes < lastBytes {
								prevFiles += lastBytes
								lastFile = output.Name
							}
							lastBytes = output.Bytes
							delta := prevFiles + lastBytes - totalProgress
							totalProgress = bar.Add64(delta)
						}

						if progress {
							bar.Update()
						}
					case size := <-sizeChan:
						if progress {
							bar.Total = size
							bar.ShowPercent = true
							bar.ShowBar = true
							bar.ShowTimeLeft = true
						}
					case <-req.Context.Done():
						// don't set or print error here, that happens in the goroutine below
						return
					}
				}

				if progress && bar.Total == 0 && bar.Get() != 0 {
					bar.Total = bar.Get()
					bar.ShowPercent = true
					bar.ShowBar = true
					bar.ShowTimeLeft = true
					bar.Update()
				}
			}

			if e := res.Error(); e != nil {
				close(outChan)
				return e
			}

			wait := make(chan struct{})
			go progressBar(wait)

			defer func() { <-wait }()
			defer close(outChan)

			for {
				v, err := res.Next()
				if err != nil {
					if err == io.EOF {
						return nil
					}

					return err
				}

				select {
				case outChan <- v:
				case <-req.Context.Done():
					return req.Context.Err()
				}
			}
		},
	},
	Type: AddEvent{},
}
