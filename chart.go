package main

import (
	"time"

	"github.com/dustin/go-humanize"
	"github.com/mum4k/termdash"
	"github.com/mum4k/termdash/cell"
	"github.com/mum4k/termdash/container"
	"github.com/mum4k/termdash/container/grid"
	"github.com/mum4k/termdash/linestyle"
	"github.com/mum4k/termdash/terminal/termbox"
	"github.com/mum4k/termdash/terminal/terminalapi"
	"github.com/mum4k/termdash/widgets/linechart"
	"github.com/mum4k/termdash/widgets/text"
)

const TUI_GRAPH_MAX_POINTS = 20000
const REDRAW_INTERVAL = 250 * time.Millisecond

type chart struct {
	t               *termbox.Terminal
	container       *container.Container
	graphDataRx     []float64
	graphDataRxTime []string
	graphDataTx     []float64
	graphDataTxTime []string
	lcRx            *linechart.LineChart
	lcTx            *linechart.LineChart
}

func newChart(showRx, showTx bool) (*chart, error) {
	t, err := termbox.New()
	if err != nil {
		return nil, err
	}

	c := &chart{
		t: t,
	}

	builder := grid.New()

	if showRx {
		lcRx, err := linechart.New(
			linechart.AxesCellOpts(cell.FgColor(cell.ColorRed)),
			linechart.YLabelCellOpts(cell.FgColor(cell.ColorGreen)),
			linechart.XLabelCellOpts(cell.FgColor(cell.ColorGreen)),
			linechart.YAxisFormattedValues(func(v float64) string {
				return humanize.Bytes(uint64(v))
			}),
		)
		if err != nil {
			return nil, err
		}

		rxLegend, err := text.New(text.RollContent(), text.WrapAtWords())
		if err != nil {
			return nil, err
		}
		if err := rxLegend.Write("rx"); err != nil {
			return nil, err
		}

		builder.Add(
			grid.RowHeightPerc(
				50,
				grid.ColWidthPerc(99,
					grid.Widget(lcRx,
						container.Border(linestyle.Light),
						container.BorderTitle("RX"),
						container.BorderTitleAlignCenter())),
			))

		c.lcRx = lcRx
		c.graphDataRx = make([]float64, 0, TUI_GRAPH_MAX_POINTS)
		c.graphDataRxTime = make([]string, 0, TUI_GRAPH_MAX_POINTS)
	}

	if showTx {
		lcTx, err := linechart.New(
			linechart.AxesCellOpts(cell.FgColor(cell.ColorRed)),
			linechart.YLabelCellOpts(cell.FgColor(cell.ColorGreen)),
			linechart.XLabelCellOpts(cell.FgColor(cell.ColorGreen)),
			linechart.YAxisFormattedValues(func(v float64) string {
				return humanize.Bytes(uint64(v))
			}),
		)
		if err != nil {
			return nil, err
		}

		txLegend, err := text.New(text.RollContent(), text.WrapAtWords())
		if err != nil {
			return nil, err
		}
		if err := txLegend.Write("tx"); err != nil {
			return nil, err
		}

		builder.Add(
			grid.RowHeightPerc(
				50,
				grid.ColWidthPerc(99,
					grid.Widget(lcTx,
						container.Border(linestyle.Light),
						container.BorderTitle("TX"),
						container.BorderTitleAlignCenter())),
			))
		c.lcTx = lcTx
		c.graphDataTx = make([]float64, 0, TUI_GRAPH_MAX_POINTS)
		c.graphDataTxTime = make([]string, 0, TUI_GRAPH_MAX_POINTS)
	}

	gridOpts, err := builder.Build()
	if err != nil {
		return nil, err
	}
	con, err := container.New(
		c.t,
		append(gridOpts,
			container.Border(linestyle.Light),
			container.BorderTitle("PRESS Q TO QUIT"))...,
	)
	if err != nil {
		return nil, err
	}
	c.container = con

	return c, nil
}

func (c *chart) run() {
	quitter := func(k *terminalapi.Keyboard) {
		if k.Key == 'q' || k.Key == 'Q' {
			cancel()
		}
	}

	if err := termdash.Run(ctx, c.t, c.container, termdash.KeyboardSubscriber(quitter), termdash.RedrawInterval(REDRAW_INTERVAL)); err != nil {
		panic(err)
	}
}

func (c *chart) updateRxData(rx uint64, n time.Time) {
	if c.graphDataRx == nil {
		return
	}

	if len(c.graphDataRx) < TUI_GRAPH_MAX_POINTS {
		c.graphDataRx = append(c.graphDataRx, float64(rx))
		c.graphDataRxTime = append(c.graphDataRxTime, n.Format("15:04:05.000"))
	} else {
		c.graphDataRx = append(c.graphDataRx[1:], float64(rx))
		c.graphDataRxTime = append(c.graphDataRxTime[1:], n.Format("15:04:05.000"))
	}

	if err := c.lcRx.Series("rx", c.graphDataRx,
		linechart.SeriesCellOpts(cell.FgColor(cell.ColorGreen)),
		linechart.SeriesXLabels(timeToMapForSeriesXLabels(c.graphDataRxTime)),
	); err != nil {
		panic(err)
	}
}

func (c *chart) updateTxData(tx uint64, n time.Time) {
	if c.graphDataTx == nil {
		return
	}

	if len(c.graphDataTx) < TUI_GRAPH_MAX_POINTS {
		c.graphDataTx = append(c.graphDataTx, float64(tx))
		c.graphDataTxTime = append(c.graphDataTxTime, n.Format("15:04:05.000"))
	} else {
		c.graphDataTx = append(c.graphDataTx[1:], float64(tx))
		c.graphDataTxTime = append(c.graphDataTxTime[1:], n.Format("15:04:05.000"))
	}

	if err := c.lcTx.Series("tx", c.graphDataTx,
		linechart.SeriesCellOpts(cell.FgColor(cell.ColorGreen)),
		linechart.SeriesXLabels(timeToMapForSeriesXLabels(c.graphDataTxTime)),
	); err != nil {
		panic(err)
	}
}

func (c *chart) stop() {
	c.t.Close()
}

// func handleStats() {
// 	var lastRx, lastTx uint64
// 	var graphDataRx, graphDataTx []float64
// 	var graphDataRxTime, graphDataTxTime []string
// 	var lcRx, lcTx *linechart.LineChart

// 	dash := "-"

// 	if showGraph {
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
// 		}
// 	}
// }

func timeToMapForSeriesXLabels(t []string) map[int]string {
	if true {
		return nil
	}
	m := make(map[int]string)
	for i, v := range t {
		if v == "" {
			m[i] = " "
		} else {
			m[i] = v
		}
	}
	return m
}

// func getRxTxValues(txrxInfo *bpf.BPFMap) (uint64, uint64, error) {
// 	var received, transferred uint64

// 	if trackRx {
// 		key := 0
// 		values := make([]byte, 8*runtime.NumCPU())
// 		err := txrxInfo.GetValueReadInto(unsafe.Pointer(&key), &values)
// 		if err != nil {
// 			return 0, 0, err
// 		}
// 		last := 0
// 		for i := 0; i < runtime.NumCPU(); i++ {
// 			cnt := binary.LittleEndian.Uint64(values[last : last+8])
// 			last += 8
// 			received += cnt
// 		}
// 	}

// 	if trackTx {
// 		key := 1
// 		values := make([]byte, 8*runtime.NumCPU())
// 		err := txrxInfo.GetValueReadInto(unsafe.Pointer(&key), &values)
// 		if err != nil {
// 			return 0, 0, err
// 		}

// 		last := 0
// 		for i := 0; i < runtime.NumCPU(); i++ {
// 			cnt := binary.LittleEndian.Uint64(values[last : last+8])
// 			last += 8
// 			transferred += uint64(cnt)
// 		}
// 	}

// 	return received, transferred, nil
// }
