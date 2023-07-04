package main

import (
	"bytes"
	"container/ring"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
	"github.com/go-echarts/go-echarts/v2/charts"
	"github.com/go-echarts/go-echarts/v2/components"
	"github.com/go-echarts/go-echarts/v2/opts"
)

var (
	rxHist, txHist *hdrhistogram.Histogram
	rxData, txData *ring.Ring
)

func statsInit() {
	if printHistogram {
		rxHist = hdrhistogram.New(1, int64(10000000000), 3)
		txHist = hdrhistogram.New(1, int64(10000000000), 3)
	}
	if saveGraphHtmlPath != "" {
		rxData = ring.New(10000)
		txData = ring.New(10000)
	}
}

func statsFinish() {
	if printHistogram {
		fmt.Println("Received (in KiB)")
		fmt.Println(getHistogram(rxHist))

		fmt.Println("Transferred (in KiB)")
		fmt.Println(getHistogram(txHist))
	}

	if saveGraphHtmlPath != "" {
		saveGraph()
	}
}

func statsHandleRxData(t time.Time, rxbytes uint64) {
	if rxHist != nil {
		rxHist.RecordValue(int64(rxbytes / 1024))
	}

	if rxData != nil {
		rxData.Value = statData{t, rxbytes / 1024}
		rxData = rxData.Next()
	}
}

func statsHandleTxData(t time.Time, txbytes uint64) {
	if txHist != nil {
		txHist.RecordValue(int64(txbytes / 1024))
	}

	if txData != nil {
		txData.Value = statData{t, txbytes / 1024}
		txData = txData.Next()
	}
}

type statData struct {
	t time.Time
	v uint64
}

func saveGraph() {
	page := components.NewPage()
	page.SetLayout(components.PageFlexLayout)
	page.AddCharts(
		getRxScatter(),
		getTxScatter(),
	)
	f, err := os.Create(saveGraphHtmlPath)
	if err != nil {
		panic(err)
	}
	page.Render(io.MultiWriter(f))
	log.Printf("saved graph at %s\n", saveGraphHtmlPath)
}

func getRxScatter() *charts.Scatter {
	scatter := newScatter("Data receive")

	var x []string
	var d []opts.ScatterData
	rxData.Do(func(p any) {
		if p == nil {
			return
		}
		rxd := p.(statData)
		x = append(x, rxd.t.Format("15:04:05.000"))
		d = append(d, opts.ScatterData{
			Value:        rxd.v,
			Symbol:       "roundRect",
			SymbolSize:   5,
			SymbolRotate: 0,
		})
	})

	scatter.SetXAxis(x).
		AddSeries("Receive", d)

	return scatter
}

func getTxScatter() *charts.Scatter {
	scatter := newScatter("Data transfer")

	var x []string
	var d []opts.ScatterData
	txData.Do(func(p any) {
		if p == nil {
			return
		}
		txd := p.(statData)
		x = append(x, txd.t.Format("15:04:05.000"))
		d = append(d, opts.ScatterData{
			Value:        txd.v,
			Symbol:       "roundRect",
			SymbolSize:   5,
			SymbolRotate: 0,
		})
	})

	scatter.SetXAxis(x).
		AddSeries("Transfer", d)

	return scatter
}

func newScatter(title string) *charts.Scatter {
	scatter := charts.NewScatter()
	scatter.SetGlobalOptions(
		charts.WithTitleOpts(opts.Title{Title: title}),
		charts.WithLegendOpts(opts.Legend{Type: "scroll"}),
		charts.WithXAxisOpts(opts.XAxis{Name: "Time", Show: true}),
		charts.WithYAxisOpts(opts.YAxis{Name: "Bytes (KB)", Show: true}),
		charts.WithDataZoomOpts(opts.DataZoom{
			Type:  "slider",
			Start: 0,
			End:   10,
		}),
		charts.WithInitializationOpts(opts.Initialization{PageTitle: "Network microburst charts"}),
		charts.WithTooltipOpts(opts.Tooltip{Show: true, Trigger: "item", TriggerOn: "mousemove|click", Enterable: true}),
		charts.WithToolboxOpts(opts.Toolbox{Show: true, Feature: &opts.ToolBoxFeature{
			DataZoom: &opts.ToolBoxFeatureDataZoom{
				Show: true,
				Title: map[string]string{
					"zoom": "zoom",
					"back": "back",
				}}}}),
	)
	return scatter
}

type Bucket struct {
	Interval float64
	Count    int64
	Percent  float64
}

var barChar = "■"

func getHistogram(hdrhist *hdrhistogram.Histogram) string {
	var o strings.Builder
	buckets := getHistogramBuckets(hdrhist)
	if len(buckets) == 0 {
		log.Printf("No histogram buckets")
		return o.String()
	}

	//fmt.Fprintf(&o, "\n%v:\n", title)
	fmt.Fprint(&o, getResponseHistogram(buckets))

	return o.String()
}

func getResponseHistogram(buckets []Bucket) string {
	var maxCount int64
	for _, b := range buckets {
		if b.Count > maxCount {
			maxCount = b.Count
		}
	}

	res := new(bytes.Buffer)
	for i := 0; i < len(buckets); i++ {
		var barLen int64
		if maxCount > 0 {
			barLen = (buckets[i].Count*40 + maxCount/2) / maxCount
		}
		res.WriteString(fmt.Sprintf("%15.3f [%10d]\t|%v\n", buckets[i].Interval, buckets[i].Count, strings.Repeat(barChar, int(barLen))))
	}

	return res.String()
}

func getHistogramBuckets(hdrhist *hdrhistogram.Histogram) []Bucket {
	var bars []hdrhistogram.Bar
	b := hdrhist.Distribution()
	for _, v := range b {
		if v.Count > 0 {
			bars = append(bars, v)
		}
	}

	if len(bars) == 0 {
		return []Bucket{}
	}

	min := hdrhist.Min()
	max := hdrhist.Max()

	bc := int64(20)
	buckets := make([]int64, bc+1)
	counts := make([]int64, bc+1)
	bs := (max - min) / (bc)
	for i := int64(0); i < bc; i++ {
		buckets[i] = min + bs*(i)
	}

	buckets[bc] = max
	counts[bc] = bars[len(bars)-1].Count

	// TODO: Figure out a better way to map hdrhistogram Bars into our
	// buckets here.
	bi := 0
	for i := 0; i < len(bars)-1; {
		if bars[i].From >= buckets[bi] && bars[i].To <= buckets[bi] {
			counts[bi] += bars[i].Count
			i++
		} else if bars[i].From <= buckets[bi] {
			// TODO: Properly handle overlapping buckets
			id := bi - 1
			if id < 0 {
				id = 0
			}
			counts[id] += bars[i].Count
			i++
		} else if bi < len(buckets)-1 {
			bi++
		}
	}

	var total int64
	for i := 0; i < len(buckets); i++ {
		total += counts[i]
	}

	res := []Bucket{}
	for i := 0; i < len(buckets); i++ {
		if counts[i] > 0 {
			res = append(res,
				Bucket{
					Interval: float64(buckets[i]),
					Count:    counts[i],
					Percent:  100.0 * float64(counts[i]) / float64(total),
				})
		}
	}

	return res
}

// import (
// 	"bytes"
// 	"context"
// 	"encoding/json"
// 	"fmt"
// 	"net/http"
// 	"net/http/pprof"
// 	"runtime"
// 	"text/template"
// 	"time"

// 	"github.com/go-echarts/go-echarts/v2/charts"
// 	"github.com/go-echarts/go-echarts/v2/components"
// 	"github.com/go-echarts/go-echarts/v2/opts"
// 	"github.com/go-echarts/go-echarts/v2/templates"
// 	"github.com/go-echarts/go-echarts/v2/types"
// 	"github.com/rs/cors"

// 	"github.com/go-echarts/statsview/statics"
// )

// // ViewManager
// type ViewManager struct {
// 	srv *http.Server

// 	Smgr   *StatsMgr
// 	Ctx    context.Context
// 	Cancel context.CancelFunc
// 	Views  []Viewer
// }

// // Register registers views to the ViewManager
// func (vm *ViewManager) Register(views ...Viewer) {
// 	vm.Views = append(vm.Views, views...)

// }

// // Start runs a http server and begin to collect metrics
// func (vm *ViewManager) Start() error {
// 	return vm.srv.ListenAndServe()
// }

// // Stop shutdown the http server gracefully
// func (vm *ViewManager) Stop() {
// 	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
// 	defer cancel()
// 	vm.srv.Shutdown(ctx)
// 	vm.Cancel()
// }

// func init() {
// 	templates.PageTpl = `
// {{- define "page" }}
// <!DOCTYPE html>
// <html>
//     {{- template "header" . }}
// <body>
// <p>&nbsp;&nbsp;🚀 <a href="https://github.com/go-echarts/statsview"><b>StatsView</b></a> <em>is a real-time Golang runtime stats visualization profiler</em></p>
// <style> .box { justify-content:center; display:flex; flex-wrap:wrap } </style>
// <div class="box"> {{- range .Charts }} {{ template "base" . }} {{- end }} </div>
// </body>
// </html>
// {{ end }}
// `
// }

// // New creates a new ViewManager instance
// func New() *ViewManager {
// 	page := components.NewPage()
// 	page.PageTitle = "Statsview"
// 	page.AssetsHost = fmt.Sprintf("http://%s/debug/statsview/statics/", defaultCfg.LinkAddr)
// 	page.Assets.JSAssets.Add("jquery.min.js")

// 	mgr := &ViewManager{
// 		srv: &http.Server{
// 			Addr:           defaultCfg.ListenAddr,
// 			ReadTimeout:    time.Minute,
// 			WriteTimeout:   time.Minute,
// 			MaxHeaderBytes: 1 << 20,
// 		},
// 	}
// 	mgr.Ctx, mgr.Cancel = context.WithCancel(context.Background())
// 	mgr.Register(
// 		NewGoroutinesViewer(),
// 	)
// 	smgr := NewStatsMgr(mgr.Ctx)
// 	for _, v := range mgr.Views {
// 		v.SetStatsMgr(smgr)
// 	}

// 	mux := http.NewServeMux()
// 	mux.HandleFunc("/debug/pprof/", pprof.Index)
// 	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
// 	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
// 	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
// 	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

// 	for _, v := range mgr.Views {
// 		page.AddCharts(v.View())
// 		mux.HandleFunc("/debug/statsview/view/"+v.Name(), v.Serve)
// 	}

// 	mux.HandleFunc("/debug/statsview", func(w http.ResponseWriter, _ *http.Request) {
// 		page.Render(w)
// 	})

// 	staticsPrev := "/debug/statsview/statics/"
// 	mux.HandleFunc(staticsPrev+"echarts.min.js", func(w http.ResponseWriter, _ *http.Request) {
// 		w.Write([]byte(statics.EchartJS))
// 	})

// 	mux.HandleFunc(staticsPrev+"jquery.min.js", func(w http.ResponseWriter, _ *http.Request) {
// 		w.Write([]byte(statics.JqueryJS))
// 	})

// 	mux.HandleFunc(staticsPrev+"themes/westeros.js", func(w http.ResponseWriter, _ *http.Request) {
// 		w.Write([]byte(statics.WesterosJS))
// 	})

// 	mux.HandleFunc(staticsPrev+"themes/macarons.js", func(w http.ResponseWriter, _ *http.Request) {
// 		w.Write([]byte(statics.MacaronsJS))
// 	})

// 	mgr.srv.Handler = cors.AllowAll().Handler(mux)
// 	return mgr
// }

// type Viewer interface {
// 	Name() string
// 	View() *charts.Line
// 	Serve(w http.ResponseWriter, _ *http.Request)
// 	SetStatsMgr(smgr *StatsMgr)
// }

// type StatsMgr struct {
// 	last   int64
// 	Ctx    context.Context
// 	Cancel context.CancelFunc
// }

// func NewStatsMgr(ctx context.Context) *StatsMgr {
// 	s := &StatsMgr{}
// 	s.Ctx, s.Cancel = context.WithCancel(ctx)
// 	go s.polling()

// 	return s
// }

// func (s *StatsMgr) Tick() {
// 	s.last = time.Now().Unix() + int64(float64(Interval())/1000.0)*2
// }

// func (s *StatsMgr) polling() {
// 	ticker := time.NewTicker(time.Duration(Interval()) * time.Millisecond)
// 	defer ticker.Stop()

// 	for {
// 		select {
// 		case <-ticker.C:
// 			if s.last > time.Now().Unix() {
// 				runtime.ReadMemStats(memstats.Stats)
// 				memstats.T = time.Now().Format(defaultCfg.TimeFormat)
// 			}
// 		case <-s.Ctx.Done():
// 			return
// 		}
// 	}
// }

// func Interval() int {
// 	return defaultCfg.Interval
// }

// type statsEntity struct {
// 	Stats *runtime.MemStats
// 	T     string
// }

// var memstats = &statsEntity{Stats: &runtime.MemStats{}}

// // GoroutinesViewer collects the goroutine number metric via `runtime.NumGoroutine()`
// type GoroutinesViewer struct {
// 	smgr  *StatsMgr
// 	graph *charts.Line
// }

// func (vr *GoroutinesViewer) SetStatsMgr(smgr *StatsMgr) {
// 	vr.smgr = smgr
// }

// func (vr *GoroutinesViewer) Name() string {
// 	return "goroutine"
// }

// func (vr *GoroutinesViewer) View() *charts.Line {
// 	return vr.graph
// }

// type Metrics struct {
// 	Values []float64 `json:"values"`
// 	Time   string    `json:"time"`
// }

// func (vr *GoroutinesViewer) Serve(w http.ResponseWriter, _ *http.Request) {
// 	vr.smgr.Tick()

// 	metrics := Metrics{
// 		Values: []float64{float64(runtime.NumGoroutine())},
// 		Time:   time.Now().Format(defaultCfg.TimeFormat),
// 	}

// 	bs, _ := json.Marshal(metrics)
// 	w.Write(bs)
// }

// func NewGoroutinesViewer() Viewer {
// 	graph := newBasicView("goroutine")
// 	graph.SetGlobalOptions(
// 		charts.WithYAxisOpts(opts.YAxis{Name: "Num"}),
// 		charts.WithTitleOpts(opts.Title{Title: "Goroutines"}),
// 	)
// 	graph.AddSeries("Goroutines", []opts.LineData{})

// 	return &GoroutinesViewer{graph: graph}
// }

// func newBasicView(route string) *charts.Line {
// 	graph := charts.NewLine()
// 	graph.SetGlobalOptions(
// 		charts.WithLegendOpts(opts.Legend{Show: true}),
// 		charts.WithTooltipOpts(opts.Tooltip{Show: true, Trigger: "axis"}),
// 		charts.WithXAxisOpts(opts.XAxis{Name: "Time"}),
// 		charts.WithInitializationOpts(opts.Initialization{
// 			Width:  "600px",
// 			Height: "400px",
// 			Theme:  string(defaultCfg.Theme),
// 		}),
// 	)
// 	graph.SetXAxis([]string{}).SetSeriesOptions(charts.WithLineChartOpts(opts.LineChart{Smooth: true}))
// 	graph.AddJSFuncs(genViewTemplate(graph.ChartID, route))
// 	return graph
// }

// func genViewTemplate(vid, route string) string {
// 	tpl, err := template.New("view").Parse(defaultCfg.Template)
// 	if err != nil {
// 		panic("statsview: failed to parse template " + err.Error())
// 	}

// 	var c = struct {
// 		Interval  int
// 		MaxPoints int
// 		Addr      string
// 		Route     string
// 		ViewID    string
// 	}{
// 		Interval:  defaultCfg.Interval,
// 		MaxPoints: defaultCfg.MaxPoints,
// 		Addr:      defaultCfg.LinkAddr,
// 		Route:     route,
// 		ViewID:    vid,
// 	}

// 	buf := bytes.Buffer{}
// 	if err := tpl.Execute(&buf, c); err != nil {
// 		panic("statsview: failed to execute template " + err.Error())
// 	}

// 	return buf.String()
// }

// const (
// 	DefaultTemplate = `
// $(function () { setInterval({{ .ViewID }}_sync, {{ .Interval }}); });
// function {{ .ViewID }}_sync() {
//     $.ajax({
//         type: "GET",
//         url: "http://{{ .Addr }}/debug/statsview/view/{{ .Route }}",
//         dataType: "json",
//         success: function (result) {
//             let opt = goecharts_{{ .ViewID }}.getOption();

//             let x = opt.xAxis[0].data;
//             x.push(result.time);
//             if (x.length > {{ .MaxPoints }}) {
//                 x = x.slice(1);
//             }
//             opt.xAxis[0].data = x;

//             for (let i = 0; i < result.values.length; i++) {
//                 let y = opt.series[i].data;
//                 y.push({ value: result.values[i] });
//                 if (y.length > {{ .MaxPoints }}) {
//                     y = y.slice(1);
//                 }
//                 opt.series[i].data = y;

//                 goecharts_{{ .ViewID }}.setOption(opt);
//             }
//         }
//     });
// }`
// 	DefaultMaxPoints  = 30
// 	DefaultTimeFormat = "15:04:05"
// 	DefaultInterval   = 2000
// 	DefaultAddr       = "localhost:18066"
// 	DefaultTheme      = ThemeMacarons
// )

// var defaultCfg = &config{
// 	Interval:   DefaultInterval,
// 	MaxPoints:  DefaultMaxPoints,
// 	Template:   DefaultTemplate,
// 	ListenAddr: DefaultAddr,
// 	LinkAddr:   DefaultAddr,
// 	TimeFormat: DefaultTimeFormat,
// 	Theme:      DefaultTheme,
// }

// type config struct {
// 	Interval   int
// 	MaxPoints  int
// 	Template   string
// 	ListenAddr string
// 	LinkAddr   string
// 	TimeFormat string
// 	Theme      Theme
// }

// type Theme string

// const (
// 	ThemeWesteros Theme = types.ThemeWesteros
// 	ThemeMacarons Theme = types.ThemeMacarons
// )
