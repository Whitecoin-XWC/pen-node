// Copyright 2020 The Penguin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"fmt"
	pen "github.com/penguintop/penguin"
	"github.com/penguintop/penguin/pkg/property"
	"github.com/penguintop/penguin/pkg/xwcfmt"
	"io/ioutil"

	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/accounts/external"
	"github.com/penguintop/penguin/pkg/crypto"
	"github.com/penguintop/penguin/pkg/keystore"
	filekeystore "github.com/penguintop/penguin/pkg/keystore/file"
	memkeystore "github.com/penguintop/penguin/pkg/keystore/mem"
	"github.com/penguintop/penguin/pkg/logging"
	"github.com/penguintop/penguin/pkg/node"
	"github.com/penguintop/penguin/pkg/resolver/multiresolver"
	"github.com/penguintop/penguin/pkg/penguin"
	"github.com/kardianos/service"
	"github.com/spf13/cobra"
)

const (
	serviceName = "PenguinPenSvc"
)

func (c *command) initStartCmd() (err error) {

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a Penguin node",
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			if len(args) > 0 {
				return cmd.Help()
			}

			v := strings.ToLower(c.config.GetString(optionNameVerbosity))
			logger, err := newLogger(cmd, v)
			if err != nil {
				return fmt.Errorf("new logger: %v", err)
			}

			isWindowsService, err := isWindowsService()
			if err != nil {
				return fmt.Errorf("failed to determine if we are running in service: %w", err)
			}

			if isWindowsService {
				var err error
				logger, err = createWindowsEventLogger(serviceName, logger)
				if err != nil {
					return fmt.Errorf("failed to create windows logger %w", err)
				}
			}

			// If the resolver is specified, resolve all connection strings
			// and fail on any errors.
			var resolverCfgs []multiresolver.ConnectionConfig
			resolverEndpoints := c.config.GetStringSlice(optionNameResolverEndpoints)
			if len(resolverEndpoints) > 0 {
				resolverCfgs, err = multiresolver.ParseConnectionStrings(resolverEndpoints)
				if err != nil {
					return err
				}
			}

			penASCII := `
Welcome to the Penguin....

       /~~~~~~\
      /'    -s- ~~~~\
     /'dHHb      ~~~~
    /'dHHHA     :
   /' VHHHHaadHHb:
  /'   VHHHHHHHHb:
 /'      VHHHHHHH:
/'        dHHHHHHH:
|        dHHHHHHHH:
|       dHHHHHHHHH:
|       VHHHHHHHHH:
|   b    HHHHHHHHV:
|   Hb   HHHHHHHV'
|   HH  dHHHHHHV'
|   VHbdHHHHHHV'
|    VHHHHHHHV'
\    VHHHHHHH:
 \oodboooooodH
HHHHHHHHHHHHHHHHHHHHH

		   `
			fmt.Println(penASCII)
			logger.Infof("version: %v", pen.Version)

			debugAPIAddr := c.config.GetString(optionNameDebugAPIAddr)
			if !c.config.GetBool(optionNameDebugAPIEnable) {
				debugAPIAddr = ""
			}

			signerConfig, err := c.configureSigner(cmd, logger)
			if err != nil {
				return err
			}

			bootNode := c.config.GetBool(optionNameBootnodeMode)
			fullNode := c.config.GetBool(optionNameFullNode)
			auditNode := c.config.GetBool(optionNameAuditMode)

			if bootNode && !fullNode {
				return errors.New("boot node must be started as a full node")
			}

			b, err := node.NewPen(c.config.GetString(optionNameP2PAddr), signerConfig.address, *signerConfig.publicKey, signerConfig.signer, uint64(property.CHAIN_ID_NUM), logger, signerConfig.libp2pPrivateKey, signerConfig.pssPrivateKey, node.Options{
				DataDir:                  c.config.GetString(optionNameDataDir),
				CacheCapacity:            c.config.GetUint64(optionNameCacheCapacity),
				DBOpenFilesLimit:         c.config.GetUint64(optionNameDBOpenFilesLimit),
				DBBlockCacheCapacity:     c.config.GetUint64(optionNameDBBlockCacheCapacity),
				DBWriteBufferSize:        c.config.GetUint64(optionNameDBWriteBufferSize),
				DBDisableSeeksCompaction: c.config.GetBool(optionNameDBDisableSeeksCompaction),
				APIAddr:                  c.config.GetString(optionNameAPIAddr),
				DebugAPIAddr:             debugAPIAddr,
				Addr:                     c.config.GetString(optionNameP2PAddr),
				NATAddr:                  c.config.GetString(optionNameNATAddr),
				EnableWS:                 c.config.GetBool(optionNameP2PWSEnable),
				EnableQUIC:               c.config.GetBool(optionNameP2PQUICEnable),
				WelcomeMessage:           c.config.GetString(optionWelcomeMessage),
				Bootnodes:                c.config.GetStringSlice(optionNameBootnodes),
				CORSAllowedOrigins:       c.config.GetStringSlice(optionCORSAllowedOrigins),
				Standalone:               c.config.GetBool(optionNameStandalone),
				TracingEnabled:           c.config.GetBool(optionNameTracingEnabled),
				TracingEndpoint:          c.config.GetString(optionNameTracingEndpoint),
				TracingServiceName:       c.config.GetString(optionNameTracingServiceName),
				Logger:                   logger,
				GlobalPinningEnabled:     c.config.GetBool(optionNameGlobalPinningEnabled),
				PaymentThreshold:         c.config.GetString(optionNamePaymentThreshold),
				PaymentTolerance:         c.config.GetString(optionNamePaymentTolerance),
				PaymentEarly:             c.config.GetString(optionNamePaymentEarly),
				ResolverConnectionCfgs:   resolverCfgs,
				GatewayMode:              c.config.GetBool(optionNameGatewayMode),
				BootnodeMode:             bootNode,
				SwapEndpoint:             c.config.GetString(optionNameSwapEndpoint),
				//SwapFactoryAddress:         c.config.GetString(optionNameSwapFactoryAddress),
				SwapFactoryAddress:         "",
				SwapLegacyFactoryAddresses: c.config.GetStringSlice(optionNameSwapLegacyFactoryAddresses),
				SwapInitialDeposit:         c.config.GetString(optionNameSwapInitialDeposit),
				SwapEnable:                 c.config.GetBool(optionNameSwapEnable),
				FullNodeMode:               fullNode,
				Transaction:                c.config.GetString(optionNameTransactionHash),
				PostageContractAddress:     c.config.GetString(optionNamePostageContractAddress),
				BlockTime:                  c.config.GetUint64(optionNameBlockTime),
				DeployGasPrice:             c.config.GetString(optionNameSwapDeploymentGasPrice),

				//
				AuditNodeMode: auditNode,
				AuditEndpoint: c.config.GetString(optionNameAuditEndpoints),
			})
			if err != nil {
				return err
			}

			// Wait for termination or interrupt signals.
			// We want to clean up things at the end.
			interruptChannel := make(chan os.Signal, 1)
			signal.Notify(interruptChannel, syscall.SIGINT, syscall.SIGTERM)

			p := &program{
				start: func() {
					// Block main goroutine until it is interrupted
					sig := <-interruptChannel

					logger.Debugf("received signal: %v", sig)
					logger.Info("shutting down")
				},
				stop: func() {
					// Shutdown
					done := make(chan struct{})
					go func() {
						defer close(done)

						ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
						defer cancel()

						if err := b.Shutdown(ctx); err != nil {
							logger.Errorf("shutdown: %v", err)
						}
					}()

					// If shutdown function is blocking too long,
					// allow process termination by receiving another signal.
					select {
					case sig := <-interruptChannel:
						logger.Debugf("received signal: %v", sig)
					case <-done:
					}
				},
			}

			if isWindowsService {
				s, err := service.New(p, &service.Config{
					Name:        serviceName,
					DisplayName: "Pen",
					Description: "Pen, Penguin client.",
				})
				if err != nil {
					return err
				}

				if err = s.Run(); err != nil {
					return err
				}
			} else {
				// start blocks until some interrupt is received
				p.start()
				p.stop()
			}

			return nil
		},
		PreRunE: func(cmd *cobra.Command, args []string) error {
			return c.config.BindPFlags(cmd.Flags())
		},
	}

	c.setAllFlags(cmd)
	c.root.AddCommand(cmd)
	return nil
}

type program struct {
	start func()
	stop  func()
}

func (p *program) Start(s service.Service) error {
	// Start should not block. Do the actual work async.
	go p.start()
	return nil
}

func (p *program) Stop(s service.Service) error {
	p.stop()
	return nil
}

type signerConfig struct {
	signer           crypto.Signer
	address          penguin.Address
	publicKey        *ecdsa.PublicKey
	libp2pPrivateKey *ecdsa.PrivateKey
	pssPrivateKey    *ecdsa.PrivateKey
}

func waitForClef(logger logging.Logger, maxRetries uint64, endpoint string) (externalSigner *external.ExternalSigner, err error) {
	for {
		externalSigner, err = external.NewExternalSigner(endpoint)
		if err == nil {
			return externalSigner, nil
		}
		if maxRetries == 0 {
			return nil, err
		}
		maxRetries--
		logger.Warningf("failing to connect to clef signer: %v", err)

		time.Sleep(5 * time.Second)
	}
}

func (c *command) configureSigner(cmd *cobra.Command, logger logging.Logger) (config *signerConfig, err error) {
	var keystore keystore.Service
	if c.config.GetString(optionNameDataDir) == "" {
		keystore = memkeystore.New()
		logger.Warning("data directory not provided, keys are not persisted")
	} else {
		keystore = filekeystore.New(filepath.Join(c.config.GetString(optionNameDataDir), "keys"))
	}

	var signer crypto.Signer
	var address penguin.Address
	var password string
	var publicKey *ecdsa.PublicKey
	if p := c.config.GetString(optionNamePassword); p != "" {
		password = p
	} else if pf := c.config.GetString(optionNamePasswordFile); pf != "" {
		b, err := ioutil.ReadFile(pf)
		if err != nil {
			return nil, err
		}
		password = string(bytes.Trim(b, "\n"))
	} else {
		// if libp2p key exists we can assume all required keys exist
		// so prompt for a password to unlock them
		// otherwise prompt for new password with confirmation to create them
		exists, err := keystore.Exists("libp2p")
		if err != nil {
			return nil, err
		}
		if exists {
			password, err = terminalPromptPassword(cmd, c.passwordReader, "Password")
			if err != nil {
				return nil, err
			}
		} else {
			password, err = terminalPromptCreatePassword(cmd, c.passwordReader)
			if err != nil {
				return nil, err
			}
		}
	}

	if c.config.GetBool(optionNameClefSignerEnable) {
		//endpoint := c.config.GetString(optionNameClefSignerEndpoint)
		//if endpoint == "" {
		//	endpoint, err = clef.DefaultIpcPath()
		//	if err != nil {
		//		return nil, err
		//	}
		//}
		//
		//externalSigner, err := waitForClef(logger, 5, endpoint)
		//if err != nil {
		//	return nil, err
		//}
		//
		//clefRPC, err := rpc.Dial(endpoint)
		//if err != nil {
		//	return nil, err
		//}
		//
		//wantedAddress := c.config.GetString(optionNameClefSignerEthereumAddress)
		//var overlayEthAddress *common.Address = nil
		//// if wantedAddress was specified use that, otherwise clef account 0 will be selected.
		//if wantedAddress != "" {
		//	ethAddress := common.HexToAddress(wantedAddress)
		//	overlayEthAddress = &ethAddress
		//}
		//
		//signer, err = clef.NewSigner(externalSigner, clefRPC, crypto.Recover, overlayEthAddress)
		//if err != nil {
		//	return nil, err
		//}
		//
		//publicKey, err = signer.PublicKey()
		//if err != nil {
		//	return nil, err
		//}
		//
		//address, err = crypto.NewOverlayAddress(*publicKey, c.config.GetUint64(optionNameNetworkID))
		//if err != nil {
		//	return nil, err
		//}
		//
		//logger.Infof("using penguin network address through clef: %s", address)

		// TODO
		return nil, fmt.Errorf("clef signer not support now")
	} else {
		logger.Warning("clef is not enabled; portability and security of your keys is sub optimal")
		penguinPrivateKey, created, err := keystore.Key("penguin", password)
		if err != nil {
			return nil, fmt.Errorf("penguin key: %w", err)
		}
		signer = crypto.NewDefaultSigner(penguinPrivateKey)
		publicKey = &penguinPrivateKey.PublicKey

		address, err = crypto.NewOverlayAddress(*publicKey, uint64(property.CHAIN_ID_NUM))
		if err != nil {
			return nil, err
		}

		if created {
			logger.Infof("new penguin network address created: %s", address)

			tempBytes := penguinPrivateKey.D.Bytes()
			var privKeyBytes [32]byte
			copy(privKeyBytes[32-len(tempBytes):], tempBytes)
			privKeyWif, err := xwcfmt.HexKeyToWifKey(hex.EncodeToString(privKeyBytes[:]))
			if err != nil {
				return nil, err
			}
			logger.Info("********************************************************************")
			logger.Infof("!!! PrivateKey: %s !!!", privKeyWif)
			logger.Infof("!!! Please backup your PrivateKey, and Do not tell it to anyone else !!!")
			logger.Info("********************************************************************")
			logger.Infof("Press any key to continue...")
			reader := bufio.NewReader(os.Stdin)
			_, _ = reader.ReadByte()

		} else {
			logger.Infof("using existing penguin network address: %s", address)
		}
	}

	logger.Infof("penguin public key %x", crypto.EncodeSecp256k1PublicKey(publicKey))

	libp2pPrivateKey, created, err := keystore.Key("libp2p", password)
	if err != nil {
		return nil, fmt.Errorf("libp2p key: %w", err)
	}
	if created {
		logger.Debugf("new libp2p key created")
	} else {
		logger.Debugf("using existing libp2p key")
	}

	pssPrivateKey, created, err := keystore.Key("pss", password)
	if err != nil {
		return nil, fmt.Errorf("pss key: %w", err)
	}
	if created {
		logger.Debugf("new pss key created")
	} else {
		logger.Debugf("using existing pss key")
	}

	logger.Infof("pss public key %x", crypto.EncodeSecp256k1PublicKey(&pssPrivateKey.PublicKey))

	// postinst and post scripts inside packaging/{deb,rpm} depend and parse on this log output
	overlayXwcAddress, err := signer.XwcAddress()
	if err != nil {
		return nil, err
	}
	xwcAddr, _ := xwcfmt.HexAddrToXwcAddr(hex.EncodeToString(overlayXwcAddress[:]))
	logger.Infof("using xwc address %s", xwcAddr)

	return &signerConfig{
		signer:           signer,
		address:          address,
		publicKey:        publicKey,
		libp2pPrivateKey: libp2pPrivateKey,
		pssPrivateKey:    pssPrivateKey,
	}, nil
}
