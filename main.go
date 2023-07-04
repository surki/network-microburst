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
	flag.Uint64Var(&rxThreshold, "rx-threshold", 0, "rx threshold for tracking, only values greater than this are tracked/printed")
	flag.Uint64Var(&txThreshold, "tx-threshold", 0, "tx threshold for tracking, only values greater than this are tracked/printed")
	flag.BoolVar(&showGraph, "show-graph", true, "plot the rate in the TUI graph")
	flag.BoolVar(&printHistogram, "print-histogram", false, "display histogram at the end")
	flag.StringVar(&saveGraphHtmlPath, "save-graph-html", "", "save the scatter plot to the given HTML file")
	flag.BoolVar(&trackRx, "track-rx", true, "track network receives")
	flag.BoolVar(&trackTx, "track-tx", true, "track network transfers")

	flag.Parse()

	statsInit()
}

func main() {
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
		go chrt.run()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		handleStats()
	}()

	// TODO: we rely on the go timer for calculating rate, so our burst
	// rate calculation is going to be only as good as its
	// granularity/accuracy. The right thing to do is to compute this in
	// the bpf code itself (using bpf_timer etc) and then publish that
	// over perf event channel.

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
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
				log.Printf("dropping stats update")
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

	dash := "-"

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
}

// func handleStats() {
// 	var lastRx, lastTx uint64
// 	var graphDataRx, graphDataTx []float64
// 	var graphDataRxTime, graphDataTxTime []string
// 	var lcRx, lcTx *linechart.LineChart

// 	dash := "-"

// 	if showGraph {
// 		t, err := termbox.New()
// 		if err != nil {
// 			panic(err)
// 		}
// 		defer func() {
// 			time.Sleep(5 * time.Second)
// 			t.Close()
// 		}()

// 		builder := grid.New()

// 		if trackRx {
// 			lcRx, err = linechart.New(
// 				linechart.AxesCellOpts(cell.FgColor(cell.ColorRed)),
// 				linechart.YLabelCellOpts(cell.FgColor(cell.ColorGreen)),
// 				linechart.XLabelCellOpts(cell.FgColor(cell.ColorGreen)),
// 				linechart.YAxisFormattedValues(func(v float64) string {
// 					return humanize.Bytes(uint64(v))
// 				}),
// 			)
// 			if err != nil {
// 				panic(err)
// 			}

// 			rxLegend, err := text.New(text.RollContent(), text.WrapAtWords())
// 			if err != nil {
// 				panic(err)
// 			}
// 			if err := rxLegend.Write("rx"); err != nil {
// 				panic(err)
// 			}

// 			builder.Add(
// 				grid.RowHeightPerc(
// 					50,
// 					grid.ColWidthPerc(99,
// 						grid.Widget(lcRx,
// 							container.Border(linestyle.Light),
// 							container.BorderTitle("RX"),
// 							container.BorderTitleAlignCenter())),
// 				))
// 		}

// 		if trackTx {
// 			lcTx, err = linechart.New(
// 				linechart.AxesCellOpts(cell.FgColor(cell.ColorRed)),
// 				linechart.YLabelCellOpts(cell.FgColor(cell.ColorGreen)),
// 				linechart.XLabelCellOpts(cell.FgColor(cell.ColorGreen)),
// 				linechart.YAxisFormattedValues(func(v float64) string {
// 					return humanize.Bytes(uint64(v))
// 				}),
// 			)
// 			if err != nil {
// 				panic(err)
// 			}

// 			txLegend, err := text.New(text.RollContent(), text.WrapAtWords())
// 			if err != nil {
// 				panic(err)
// 			}
// 			if err := txLegend.Write("tx"); err != nil {
// 				panic(err)
// 			}

// 			builder.Add(
// 				grid.RowHeightPerc(
// 					50,
// 					grid.ColWidthPerc(99,
// 						grid.Widget(lcTx,
// 							container.Border(linestyle.Light),
// 							container.BorderTitle("TX"),
// 							container.BorderTitleAlignCenter())),
// 				))
// 		}

// 		gridOpts, err := builder.Build()
// 		if err != nil {
// 			panic(err)
// 		}
// 		c, err := container.New(
// 			t,
// 			append(gridOpts,
// 				container.Border(linestyle.Light),
// 				container.BorderTitle("PRESS Q TO QUIT"))...,
// 		)
// 		if err != nil {
// 			panic(err)
// 		}

// 		quitter := func(k *terminalapi.Keyboard) {
// 			if k.Key == 'q' || k.Key == 'Q' {
// 				cancel()
// 			}
// 		}

// 		graphDataRx = make([]float64, TUI_GRAPH_MAX_POINTS, TUI_GRAPH_MAX_POINTS)
// 		graphDataRxTime = make([]string, TUI_GRAPH_MAX_POINTS, TUI_GRAPH_MAX_POINTS)
// 		graphDataTx = make([]float64, TUI_GRAPH_MAX_POINTS, TUI_GRAPH_MAX_POINTS)
// 		graphDataTxTime = make([]string, TUI_GRAPH_MAX_POINTS, TUI_GRAPH_MAX_POINTS)

// 		wg.Add(1)
// 		go func() {
// 			defer wg.Done()
// 			if err := termdash.Run(ctx, t, c, termdash.KeyboardSubscriber(quitter), termdash.RedrawInterval(250*time.Millisecond)); err != nil {
// 				panic(err)
// 			}
// 		}()
// 	}

// 	for {
// 		var currRx, currTx uint64
// 		var n time.Time

// 		select {
// 		case <-ctx.Done():
// 			return
// 		case s := <-statsChan:
// 			currRx = s.rxBytes
// 			currTx = s.txBytes
// 			n = s.time
// 		}

// 		var actRx, actTx uint64

// 		if trackRx {
// 			// TODO: handle wraparound
// 			actRx = currRx - lastRx
// 			statsHandleRxData(n, actRx)
// 			lastRx = currRx
// 		}

// 		if trackTx {
// 			actTx = currTx - lastTx
// 			statsHandleTxData(n, actTx)
// 			lastTx = currTx
// 		}

// 		if printRate {
// 			var rx, tx string
// 			var print bool
// 			if trackRx && actRx > rxThreshold {
// 				rx = humanize.Bytes(actRx)
// 				print = true
// 			} else {
// 				rx = dash
// 			}
// 			if trackTx && actTx > txThreshold {
// 				tx = humanize.Bytes(actTx)
// 				print = true
// 			} else {
// 				tx = dash
// 			}
// 			if print {
// 				fmt.Printf("%s: rx: %-10s tx: %-10s\n", n.Format("15:04:05.000"), rx, tx)
// 			}
// 		} else if showGraph {
// 			if trackRx {
// 				if len(graphDataRx) < TUI_GRAPH_MAX_POINTS {
// 					graphDataRx = append(graphDataRx, float64(actRx))
// 					graphDataRxTime = append(graphDataRxTime, n.Format("15:04:05.000"))
// 				} else {
// 					graphDataRx = append(graphDataRx[1:], float64(actRx))
// 					graphDataRxTime = append(graphDataRxTime[1:], n.Format("15:04:05.000"))
// 				}

// 				if err := lcRx.Series("rx", graphDataRx,
// 					linechart.SeriesCellOpts(cell.FgColor(cell.ColorGreen)),
// 					linechart.SeriesXLabels(timeToMapForSeriesXLabels(graphDataRxTime)),
// 				); err != nil {
// 					panic(err)
// 				}
// 			}

// 			if trackTx {
// 				if len(graphDataTx) < TUI_GRAPH_MAX_POINTS {
// 					graphDataTx = append(graphDataTx, float64(actTx))
// 					graphDataTxTime = append(graphDataTxTime, n.Format("15:04:05.000"))
// 				} else {
// 					graphDataTx = append(graphDataTx[1:], float64(actTx))
// 					graphDataTxTime = append(graphDataTxTime[1:], n.Format("15:04:05.000"))
// 				}

// 				if err := lcTx.Series("tx", graphDataTx,
// 					linechart.SeriesCellOpts(cell.FgColor(cell.ColorRed)),
// 					linechart.SeriesXLabels(timeToMapForSeriesXLabels(graphDataTxTime)),
// 				); err != nil {
// 					panic(err)
// 				}
// 			}
// 		}
// 	}
// }

// func timeToMapForSeriesXLabels(t []string) map[int]string {
// 	m := make(map[int]string)
// 	for i, v := range t {
// 		if v == "" {
// 			m[i] = " "
// 		} else {
// 			m[i] = v
// 		}
// 	}
// 	return m
// }

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
