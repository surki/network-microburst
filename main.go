package main

import (
	"C"
	"context"
	_ "embed"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	bpf "github.com/aquasecurity/libbpfgo"
	"github.com/aquasecurity/libbpfgo/helpers"
	"github.com/dustin/go-humanize"
)

var (
	debug             bool
	filterInterface   string
	burstWindow       time.Duration
	rxThreshold       uint64
	txThreshold       uint64
	printHistogram    bool
	showGraph         bool
	saveGraphHtmlPath string
	trackRx           bool
	trackTx           bool
	ctx               context.Context
	cancel            context.CancelFunc
	statsChan         = make(chan rxTxStats, 500)
	wg                sync.WaitGroup
	chrt              *chart
)

type rxTxStats struct {
	rxBytes uint64
	txBytes uint64
	time    time.Time
}

//go:embed network-microburst.bpf.o
var bpfBin []byte

const bpfName = "network-microburst.bpf.o"

func init() {
	flag.BoolVar(&debug, "debug", false, "enable debug logs")
	flag.StringVar(&filterInterface, "filter-interface", "", "network interface to track, by default all interfaces are tracked")
	flag.DurationVar(&burstWindow, "burst-window", 10*time.Millisecond, "microburst window to track, the metrics are tracked by this granularity")
	flag.BoolVar(&showGraph, "show-graph", true, "plot the rate in the TUI graph. If this is set to false, the values are printed to stdout")
	flag.Uint64Var(&rxThreshold, "print-rx-threshold", 0, "rx threshold for printing, only values greater than this are printed. used when show-graph=false")
	flag.Uint64Var(&txThreshold, "print-tx-threshold", 0, "tx threshold for printing, only values greater than this are printed. used when show-graph=false")
	flag.BoolVar(&printHistogram, "print-histogram", false, "display histogram at the end")
	flag.StringVar(&saveGraphHtmlPath, "save-graph-html", "", "save the plot to the given HTML file for offline analysis")
	flag.BoolVar(&trackRx, "track-rx", true, "track network receives")
	flag.BoolVar(&trackTx, "track-tx", true, "track network transfers")
}

func main() {
	flag.Parse()

	statsInit()

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-sig:
			cancel()
		case <-ctx.Done():
		}
	}()

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

	module, err := bpf.NewModuleFromBuffer(bpfBin, bpfName)
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

	if showGraph {
		chrt, err = newChart(trackRx, trackTx)
		if err != nil {
			panic(err)
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			chrt.run()
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		handleStats()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			// TODO: we rely on the go timer for calculating
			// rate, so our burst rate calculation is going to
			// be only as good as its granularity/accuracy. The
			// right thing to do is to compute this in the bpf
			// code itself (using bpf_timer etc) and then
			// publish that over perf event channel.
			//
			time.Sleep(burstWindow)

			select {
			case <-ctx.Done():
				return
			default:
			}

			n := time.Now()

			currRx, currTx, err := getRxTxValues(txrxInfo)
			if err != nil {
				panic(err)
			}

			select {
			case statsChan <- rxTxStats{
				rxBytes: currRx,
				txBytes: currTx,
				time:    n,
			}:
			default:
				// log.Printf("dropping stats update")
			}
		}
	}()

	<-ctx.Done()

	fmt.Println("waiting for workers to finish...")

	wg.Wait()

	if chrt != nil {
		chrt.stop()
	}

	fmt.Println("")

	statsFinish()
}

func handleStats() {
	var lastRx, lastTx uint64

	for {
		var currRx, currTx uint64
		var n time.Time

		select {
		case <-ctx.Done():
			return
		case s := <-statsChan:
			currRx = s.rxBytes
			currTx = s.txBytes
			n = s.time
		}

		// TODO: when calculating rate for the current window,
		// handle missed updates due to timer inaccuracy/misses

		var actRx, actTx uint64

		if trackRx {
			// TODO: handle wraparound
			actRx = currRx - lastRx
			statsHandleRxData(n, actRx)
			lastRx = currRx
		}

		if trackTx {
			actTx = currTx - lastTx
			statsHandleTxData(n, actTx)
			lastTx = currTx
		}

		if showGraph {
			if trackTx {
				chrt.updateTxData(actTx, n)
			}
			if trackRx {
				chrt.updateRxData(actRx, n)
			}
		} else {
			var rx, tx string
			var print bool
			if trackRx && actRx > rxThreshold {
				rx = humanize.Bytes(actRx)
				print = true
			} else {
				rx = "-"
			}
			if trackTx && actTx > txThreshold {
				tx = humanize.Bytes(actTx)
				print = true
			} else {
				tx = "-"
			}
			if print {
				fmt.Printf("%s: rx: %-10s tx: %-10s\n", n.Format("15:04:05.000"), rx, tx)
			}
		}
	}
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
