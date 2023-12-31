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
	"runtime/pprof"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/HdrHistogram/hdrhistogram-go"
	"github.com/aquasecurity/libbpfgo"
	bpf "github.com/aquasecurity/libbpfgo"
	"github.com/aquasecurity/libbpfgo/helpers"
	"github.com/dustin/go-humanize"
	"github.com/prometheus/procfs"
	"golang.org/x/sys/unix"
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
	timerHist         *hdrhistogram.Histogram
	timerToUse        string
	perfTimerCpu      int
	cpuProfile        string
	memProfile        string
)

type rxTxStats struct {
	rxBytes uint64
	txBytes uint64
	time    time.Time
}

var bpfBin []byte

//go:embed network-microburst.bpf.o
var defaultBpfBin []byte

//go:embed network-microburst.bpf.per_cpu_legacy.o
var userspaceTimerBpfBin []byte
var goTimerOnly bool
var bpfName string

const defaultBpfName = "network-microburst.bpf.o"
const userspaceTimerBpfName = "network-microburst.bpf.per_cpu_legacy.o"

func init() {
	flag.BoolVar(&debug, "debug", false, "enable debug logs")
	flag.StringVar(&filterInterface, "filter-interface", "", "network interface to track, by default all interfaces are tracked")
	flag.DurationVar(&burstWindow, "burst-window", 1*time.Millisecond, "microburst window to track, the metrics are tracked by this granularity")
	flag.BoolVar(&showGraph, "show-graph", true, "plot the rate in the TUI graph. If this is set to false, the values are printed to stdout")
	flag.Uint64Var(&rxThreshold, "print-rx-threshold", 0, "rx threshold for printing, only values greater than this are printed. used when show-graph=false")
	flag.Uint64Var(&txThreshold, "print-tx-threshold", 0, "tx threshold for printing, only values greater than this are printed. used when show-graph=false")
	flag.BoolVar(&printHistogram, "print-histogram", false, "display histogram at the end")
	flag.StringVar(&saveGraphHtmlPath, "save-graph-html", "", "save the plot to the given HTML file for offline analysis")
	flag.BoolVar(&trackRx, "track-rx", true, "track network receives")
	flag.BoolVar(&trackTx, "track-tx", true, "track network transfers")
	flag.StringVar(&timerToUse, "timer", "perf", "timer to use for tracking microbursts. can be either perf or go")
	flag.IntVar(&perfTimerCpu, "perf-cpu", -1, "cpu to use for perf timer. used only when timer=perf")
	flag.StringVar(&cpuProfile, "cpuprofile", "", "write cpu profile to `file`")
	flag.StringVar(&memProfile, "memprofile", "", "write memory profile to `file`")
	osInfo, _ := helpers.GetOSInfo()
	bpfBin = defaultBpfBin
	bpfName = defaultBpfName
	if age, _ := osInfo.CompareOSBaseKernelRelease("5.19.0"); helpers.KernelVersionNewer == age {
		fmt.Printf("warning: perf timer not supported, use kernel version older than 5.19.0. falling back to go timer.\n")
		timerToUse = "go"
		bpfBin = userspaceTimerBpfBin
		bpfName = userspaceTimerBpfName
	}
}

var btime uint64 = 0
var numCpus int = -1

func main() {
	flag.Parse()

	if cpuProfile != "" {
		f, err := os.Create(cpuProfile)
		if err != nil {
			log.Fatal("could not create CPU profile: ", err)
		}
		defer f.Close() // error handling omitted for example
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal("could not start CPU profile: ", err)
		}
		defer pprof.StopCPUProfile()
	}

	fs, err := procfs.NewFS("/proc")
	stats, err := fs.Stat()
	if err != nil {
		panic(err)
	}
	btime = stats.BootTime

	// We will calculate num cpus from /proc/cpuinfo, as we may be
	// running under taskset to run on a subset of cpus and
	// runtime.NumCPU() will not show all the physical cpus, which we
	// require in bpf code
	cpus, err := fs.CPUInfo()
	if err != nil {
		panic(err)
	}
	numCpus = len(cpus)

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
			if debug {
				log.Printf("%s", msg)
			}
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

	err = module.InitGlobalVariable("nr_cpus", uint32(numCpus))
	if err != nil {
		panic(err)
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
		if debug {
			log.Printf("attaching program %q", prog.Name())
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
		if debug {
			log.Printf("attaching program %q", prog.Name())
		}
		_, err = prog.AttachGeneric()
		if err != nil {
			panic(fmt.Sprintf("failed to attach program (%s): %v", prog.Name(), err))
		}
	}

	if debug {
		go helpers.TracePipeListen()
	}

	if showGraph {
		chrt, err = newChart(trackRx, trackTx, burstWindow)
		if err != nil {
			panic(err)
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			chrt.run()
		}()
	}
	if timerToUse == "perf" {
		perfFd, rb, err := setupPerfTimer(module)
		if err != nil {
			panic(err)
		}
		defer func() {
			if err := syscall.Close(perfFd); err != nil {
				panic(err)
			}
			rb.Close()
		}()
	} else if timerToUse == "go" {
		err := setupGoTimer(module)
		if err != nil {
			panic(err)
		}
	} else {
		panic(fmt.Sprintf("invalid timer option %q", timerToUse))
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		handleStats()
	}()

	<-ctx.Done()

	fmt.Printf("\nwaiting for workers to finish...\n")

	wg.Wait()

	if chrt != nil {
		chrt.stop()
	}

	fmt.Println("")

	statsFinish()

	if !showGraph {
		fmt.Printf("Timer accuracy: \n")
		fmt.Printf("Mean: %v StdDev: %v Min: %v Max: %v\n", time.Duration(timerHist.Mean()), time.Duration(int64(timerHist.StdDev())), time.Duration(timerHist.Min()), time.Duration(timerHist.Max()))
		fmt.Printf("Histogram:\n")
		fmt.Println(getHistogram(timerHist, func(v float64) string { return time.Duration(int64(v)).String() }))
	}

	if memProfile != "" {
		f, err := os.Create(memProfile)
		if err != nil {
			log.Fatal("could not create memory profile: ", err)
		}
		defer f.Close()
		runtime.GC()
		if err := pprof.WriteHeapProfile(f); err != nil {
			log.Fatal("could not write memory profile: ", err)
		}
	}
}

func handleStats() {
	timerHist = hdrhistogram.New(1, int64(10_000_000_000), 5)

	var lastTime time.Time

	for {
		var s rxTxStats

		select {
		case <-ctx.Done():
			return
		case s = <-statsChan:
		}

		if trackRx {
			statsHandleRxData(s.time, s.rxBytes)
		}

		if trackTx {
			statsHandleTxData(s.time, s.txBytes)
		}

		timerAccuracy := s.time.Sub(lastTime)
		lastTime = s.time
		timerHist.RecordValue(int64(timerAccuracy))

		if showGraph {
			if trackTx {
				chrt.updateTxData(s.txBytes, s.time)
			}
			if trackRx {
				chrt.updateRxData(s.rxBytes, s.time)
			}
		} else {
			var rx, tx string
			var print bool
			if trackRx && s.rxBytes > rxThreshold {
				rx = humanize.Bytes(s.rxBytes)
				print = true
			} else {
				rx = "-"
			}
			if trackTx && s.txBytes > txThreshold {
				tx = humanize.Bytes(s.txBytes)
				print = true
			} else {
				tx = "-"
			}

			if print {
				fmt.Printf("%s [%10v]: rx: %-10s tx: %-10s\n", s.time.Format("15:04:05.000"), timerAccuracy, rx, tx)
			}
		}
	}
}

func getRxTxValues(txrxInfo *bpf.BPFMap) (uint64, uint64, error) {
	var received, transferred uint64

	if trackRx {
		key := 0
		values := make([]byte, 8*numCpus)
		err := txrxInfo.GetValueReadInto(unsafe.Pointer(&key), &values)
		if err != nil {
			return 0, 0, err
		}
		last := 0
		for i := 0; i < numCpus; i++ {
			cnt := binary.LittleEndian.Uint64(values[last : last+8])
			last += 8
			received += cnt
		}
	}

	if trackTx {
		key := 1
		values := make([]byte, 8*numCpus)
		err := txrxInfo.GetValueReadInto(unsafe.Pointer(&key), &values)
		if err != nil {
			return 0, 0, err
		}

		last := 0
		for i := 0; i < numCpus; i++ {
			cnt := binary.LittleEndian.Uint64(values[last : last+8])
			last += 8
			transferred += uint64(cnt)
		}
	}

	return received, transferred, nil
}

func setupPerfTimer(module *libbpfgo.Module) (int, *libbpfgo.RingBuffer, error) {
	prog, err := module.GetProgram("calc_metrics")
	if err != nil {
		return -1, nil, fmt.Errorf("error getting program for calc_metrics: %w", err)
	}

	cpuChosen := chooseCpuForPerfTimer()
	fd, err := unix.PerfEventOpen(&unix.PerfEventAttr{
		Type:   unix.PERF_TYPE_SOFTWARE,
		Config: unix.PERF_COUNT_SW_CPU_CLOCK,
		Size:   uint32(unsafe.Sizeof(unix.PerfEventAttr{})),
		// We will use periodic sampling, we will not use the frequency
		Sample: uint64(burstWindow.Nanoseconds()),
		// Sample: uint64(1_000_0),
		// Bits:   unix.PerfBitDisabled | unix.PerfBitFreq,
	}, -1, cpuChosen, -1, 0)
	if err != nil {
		return -1, nil, fmt.Errorf("open perf event: %w", err)
	}

	_, err = prog.AttachPerfEvent(fd)
	if err != nil {
		_ = syscall.Close(fd)
		return -1, nil, fmt.Errorf("failed to attach program (%s): %v", prog.Name(), err)
	}

	if debug {
		log.Printf("setup perf timer on cpu %d with %s periodic sampling", cpuChosen, burstWindow)
	}

	eventsChannel := make(chan []byte)
	rb, err := module.InitRingBuf("events", eventsChannel)
	if err != nil {
		return -1, nil, fmt.Errorf("init ringbuf: %w", err)
	}

	rb.Poll(300)

	wg.Add(1)
	go func() {
		defer wg.Done()

		for {
			select {
			case b := <-eventsChannel:
				ts := binary.LittleEndian.Uint64(b[0:8])
				currRx := binary.LittleEndian.Uint64(b[8:16])
				currTx := binary.LittleEndian.Uint64(b[16:24])

				n := time.Unix(int64(btime), int64(ts))

				select {
				case statsChan <- rxTxStats{
					rxBytes: currRx,
					txBytes: currTx,
					time:    n,
				}:
				default:
					// log.Printf("dropping stats update")
				}

			case <-ctx.Done():
				return
			}
		}
	}()

	return fd, rb, nil
}

func setupGoTimer(module *libbpfgo.Module) error {
	txrxInfo, err := module.GetMap("txrx_info")
	if err != nil {
		return err
	}

	wg.Add(1)
	go func() {
		defer wg.Done()

		var lastRx, lastTx uint64

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

			var actRx, actTx uint64

			if trackRx {
				// TODO: handle wraparound
				actRx = currRx - lastRx
				lastRx = currRx
			}

			if trackTx {
				actTx = currTx - lastTx
				lastTx = currTx
			}

			select {
			case statsChan <- rxTxStats{
				rxBytes: actRx,
				txBytes: actTx,
				time:    n,
			}:
			default:
				// log.Printf("dropping stats update")
			}
		}
	}()

	return nil
}

func chooseCpuForPerfTimer() int {
	if perfTimerCpu != -1 {
		return perfTimerCpu
	}

	// We should choose a CPU that's not having too many irqs assigned
	// to it.  Since cpu 0 is usually the busiest, we'll probably choose
	// something from the end
	//
	// TODO: Properly choose a CPU by looking at the irq assignments,
	// cpu topology etc

	cpu := runtime.NumCPU() - 1
	if cpu < 0 {
		cpu = 0
	}

	return cpu
}
