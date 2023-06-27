package main

import (
	"C"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"time"
	"unsafe"

	bpf "github.com/aquasecurity/libbpfgo"
	"github.com/aquasecurity/libbpfgo/helpers"
	"github.com/dustin/go-humanize"
)

var (
	debug           bool
	filterInterface string
	burstWindow     time.Duration
	rxThreshold     uint64
	txThreshold     uint64
	printHistogram  bool
	printRate       bool
	saveGraphPath   string
	trackRx         bool
	trackTx         bool
)

func init() {
	flag.BoolVar(&debug, "debug", false, "enable debug logs")
	flag.StringVar(&filterInterface, "filter-interface", "", "network interface to track, by default all interfaces are tracked")
	flag.DurationVar(&burstWindow, "burst-window", 10*time.Millisecond, "microburst window to track, the metrics are tracked by this granularity")
	flag.Uint64Var(&rxThreshold, "rx-threshold", 0, "rx threshold for tracking, only values greater than this are tracked/printed")
	flag.Uint64Var(&txThreshold, "tx-threshold", 0, "tx threshold for tracking, only values greater than this are tracked/printed")
	flag.BoolVar(&printRate, "print-rate", true, "print rate on every burst window, {rx,tx}-threshold may influence what gets printed if provided")
	flag.BoolVar(&printHistogram, "print-histogram", false, "display histogram at the end")
	flag.StringVar(&saveGraphPath, "save-graph", "", "save the scatter plot to the given file")
	flag.BoolVar(&trackRx, "track-rx", true, "track network receives")
	flag.BoolVar(&trackTx, "track-tx", true, "track network transfers")

	flag.Parse()

	statsInit()
}

func main() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	bpf.SetLoggerCbs(bpf.Callbacks{
		Log: func(level int, msg string) {
			log.Printf("%s", msg)
		},
		LogFilters: []func(libLevel int, msg string) bool{
			func(libLevel int, msg string) bool {
				return debug || !(libLevel < 2)
			},
		},
	})

	module, err := bpf.NewModuleFromFile("network-microburst.bpf.o")
	if err != nil {
		panic(err)
	}
	defer module.Close()

	if filterInterface != "" {
		if len(filterInterface) > 16 {
			panic("network interfaces with more than 16 bytes not supported")
		}
		err = module.InitGlobalVariable("ifname", []byte(filterInterface))
		if err != nil {
			panic(err)
		}
		err = module.InitGlobalVariable("filter_dev", uint8(1))
		if err != nil {
			panic(err)
		}
	}

	err = module.BPFLoadObject()
	if err != nil {
		panic(err)
	}

	if trackRx {
		prog, err := module.GetProgram("trace_network_receive")
		if err != nil {
			panic(err)
		}
		_, err = prog.AttachGeneric()
		if err != nil {
			panic(fmt.Sprintf("failed to attach program (%s): %v", prog.Name(), err))
		}
	}

	if trackTx {
		prog, err := module.GetProgram("trace_network_transmit")
		if err != nil {
			panic(err)
		}
		_, err = prog.AttachGeneric()
		if err != nil {
			panic(fmt.Sprintf("failed to attach program (%s): %v", prog.Name(), err))
		}
	}

	iter := module.Iterator()
	for {
		prog := iter.NextProgram()
		if prog == nil {
			break
		}

		if prog.Name() == "" {

		}
		if debug {
			log.Printf("attaching program %q", prog.Name())
		}
	}

	if debug {
		go helpers.TracePipeListen()
	}

	txrxInfo, err := module.GetMap("txrx_info")
	if err != nil {
		panic(err)
	}

	// TODO: we rely on the go timer for calculating rate, so our burst
	// rate calculation is going to be only as good as its
	// granularity/accuracy. The right thing to do is to compute this in
	// the bpf code itself (using bpf_timer etc) and then publish that
	// over perf event channel.

	var wg sync.WaitGroup

	dash := "-"

	exit := false
	var lastRx, lastTx uint64
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			time.Sleep(burstWindow)

			if exit {
				return
			}

			n := time.Now()

			currRx, currTx, err := getRxTxValues(txrxInfo)
			if err != nil {
				panic(err)
			}

			var actRx, actTx uint64

			if trackRx {
				// TODO: handle wraparound
				actRx = currRx - lastRx
				if actRx > rxThreshold {
					statsHandleRxData(n, actRx)
				}
				lastRx = currRx
			}

			if trackTx {
				actTx = currTx - lastTx
				if actTx > txThreshold {
					statsHandleTxData(n, actTx)
				}
				lastTx = currTx
			}

			if printRate {
				var rx, tx string
				var print bool
				if trackRx && actRx > rxThreshold {
					rx = humanize.Bytes(actRx)
					print = true
				} else {
					rx = dash
				}
				if trackTx && actTx > txThreshold {
					tx = humanize.Bytes(actTx)
					print = true
				} else {
					tx = dash
				}
				if print {
					fmt.Printf("%s: rx: %-10s tx: %-10s\n", n.Format("15:04:05.000"), rx, tx)
				}
			}
		}
	}()

	<-sig
	exit = true

	wg.Wait()

	fmt.Println("")

	statsFinish()
}

func getRxTxValues(txrxInfo *bpf.BPFMap) (uint64, uint64, error) {
	var received, transferred uint64

	if trackRx {
		key := 0
		values := make([]byte, 8*runtime.NumCPU())
		err := txrxInfo.GetValueReadInto(unsafe.Pointer(&key), &values)
		if err != nil {
			return 0, 0, err
		}
		last := 0
		for i := 0; i < runtime.NumCPU(); i++ {
			cnt := binary.LittleEndian.Uint64(values[last : last+8])
			last += 8
			received += cnt
		}
	}

	if trackTx {
		key := 1
		values := make([]byte, 8*runtime.NumCPU())
		err := txrxInfo.GetValueReadInto(unsafe.Pointer(&key), &values)
		if err != nil {
			return 0, 0, err
		}

		last := 0
		for i := 0; i < runtime.NumCPU(); i++ {
			cnt := binary.LittleEndian.Uint64(values[last : last+8])
			last += 8
			transferred += uint64(cnt)
		}
	}

	return received, transferred, nil
}
