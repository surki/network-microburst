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
		// We will try to have at least last 10 seconds of data.
		// But if it crosses 1 million points, we will limit it to 1
		// million. This is to avoid the browser from hanging.
		// TODO: Maybe we can parameterize this.
		numSamplesPerSec := int(time.Second / burstWindow)
		numSamples := numSamplesPerSec * 10
		if numSamples > 1_000_000 {
			numSamples = 1_000_000
		}
		rxData = ring.New(numSamples)
		txData = ring.New(numSamples)
	}
}

func statsFinish() {
	if printHistogram {
		fmt.Println("Received (in KiB)")
		fmt.Println(getHistogram(rxHist, nil))

		fmt.Println("Transferred (in KiB)")
		fmt.Println(getHistogram(txHist, nil))
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

type intervalFormatter func(float64) string

func getHistogram(hdrhist *hdrhistogram.Histogram, interFmt intervalFormatter) string {
	var o strings.Builder
	buckets := getHistogramBuckets(hdrhist)
	if len(buckets) == 0 {
		log.Printf("No histogram buckets")
		return o.String()
	}

	//fmt.Fprintf(&o, "\n%v:\n", title)
	fmt.Fprint(&o, getResponseHistogram(buckets, interFmt))

	return o.String()
}

func getResponseHistogram(buckets []Bucket, interFmt intervalFormatter) string {
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
		if interFmt != nil {
			res.WriteString(fmt.Sprintf("%15v [%10d]\t|%v\n", interFmt(buckets[i].Interval), buckets[i].Count, strings.Repeat(barChar, int(barLen))))
		} else {
			res.WriteString(fmt.Sprintf("%15v [%10d]\t|%v\n", buckets[i].Interval, buckets[i].Count, strings.Repeat(barChar, int(barLen))))
		}
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
		if bars[i].From <= buckets[bi] && bars[i].To <= buckets[bi] {
			// Entire bar is in this bucket
			counts[bi] += bars[i].Count
			i++
		} else if bars[i].From <= buckets[bi] && bars[i].To > buckets[bi] {
			// Bar overlaps this bucket
			// Take a ratio of the count based on the overlap
			rng := bars[i].To - bars[i].From
			rng_A := buckets[bi] - bars[i].From
			rng_B := bars[i].To - buckets[bi]
			counts[bi] += int64(float64(bars[i].Count) * (float64(rng_A) / float64(rng)))
			if bi < len(buckets)-1 {
				bi++
			}
			counts[bi] += int64(float64(bars[i].Count) * (float64(rng_B) / float64(rng)))
			i++
		} else if bi < len(buckets)-1 {
			// Bar is after this bucket
			bi++
		} else {
			counts[bi] += bars[i].Count
			i++
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
