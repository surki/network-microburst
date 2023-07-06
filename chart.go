package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/mum4k/termdash"
	"github.com/mum4k/termdash/cell"
	"github.com/mum4k/termdash/container"
	"github.com/mum4k/termdash/container/grid"
	"github.com/mum4k/termdash/linestyle"
	"github.com/mum4k/termdash/terminal/tcell"
	"github.com/mum4k/termdash/terminal/terminalapi"
	"github.com/mum4k/termdash/widgets/linechart"
	"github.com/mum4k/termdash/widgets/text"
)

const TUI_GRAPH_MAX_POINTS = 20000
const REDRAW_INTERVAL = 250 * time.Millisecond

type chart struct {
	t               terminalapi.Terminal
	controller      *termdash.Controller
	container       *container.Container
	rxLock          sync.Mutex
	graphDataRx     *ringBuffer[float64]
	graphDataRxTime *ringBuffer[time.Time]
	txLock          sync.Mutex
	graphDataTx     *ringBuffer[float64]
	graphDataTxTime *ringBuffer[time.Time]
	lcRx            *linechart.LineChart
	lcTx            *linechart.LineChart
}

func newChart(showRx, showTx bool) (*chart, error) {
	t, err := tcell.New()
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

		builder.Add(
			grid.RowHeightPerc(
				50,
				grid.ColWidthPerc(99,
					grid.Widget(lcRx,
						container.Border(linestyle.Light),
						container.BorderTitle(" Received "),
						container.BorderTitleAlignCenter())),
			))

		c.lcRx = lcRx
		c.graphDataRx = newRingBuffer[float64](TUI_GRAPH_MAX_POINTS)
		c.graphDataRxTime = newRingBuffer[time.Time](TUI_GRAPH_MAX_POINTS)
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
						container.BorderTitle(" Transmitted "),
						container.BorderTitleAlignCenter())),
			))
		c.lcTx = lcTx
		c.graphDataTx = newRingBuffer[float64](TUI_GRAPH_MAX_POINTS)
		c.graphDataTxTime = newRingBuffer[time.Time](TUI_GRAPH_MAX_POINTS)
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

	ctrl, err := termdash.NewController(t, con, termdash.KeyboardSubscriber(c.kbHandler), termdash.ErrorHandler(c.termdashErrorHandler))
	if err != nil {
		return nil, err
	}
	c.controller = ctrl

	return c, nil
}

func (c *chart) run() {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(REDRAW_INTERVAL):
			if trackRx {
				y, x := c.getRxData()
				if err := c.lcRx.Series("rx", y,
					linechart.SeriesCellOpts(cell.FgColor(cell.ColorGreen)),
					linechart.SeriesXLabels(timeToMapForSeriesXLabels(x)),
				); err != nil {
					panic(err)
				}
			}
			if trackTx {
				y, x := c.getTxData()
				if err := c.lcTx.Series("tx", y,
					linechart.SeriesCellOpts(cell.FgColor(cell.ColorGreen)),
					linechart.SeriesXLabels(timeToMapForSeriesXLabels(x)),
				); err != nil {
					panic(err)
				}
			}

			if err := c.controller.Redraw(); err != nil {
				panic(err)
			}
		}
	}
}

func (c *chart) kbHandler(k *terminalapi.Keyboard) {
	if k.Key == 'q' || k.Key == 'Q' {
		cancel()
	}
}

func (c *chart) updateRxData(rx uint64, n time.Time) {
	if c.graphDataRx == nil {
		return
	}

	c.rxLock.Lock()
	defer c.rxLock.Unlock()

	c.graphDataRx.Add(float64(rx))
	c.graphDataRxTime.Add(n)
}

func (c *chart) updateTxData(tx uint64, n time.Time) {
	if c.graphDataTx == nil {
		return
	}

	c.txLock.Lock()
	defer c.txLock.Unlock()
	c.graphDataTx.Add(float64(tx))
	c.graphDataTxTime.Add(n)
}

func (c *chart) getRxData() ([]float64, []time.Time) {
	c.rxLock.Lock()
	defer c.rxLock.Unlock()

	return c.graphDataRx.Items(), c.graphDataRxTime.Items()
}

func (c *chart) getTxData() ([]float64, []time.Time) {
	c.txLock.Lock()
	defer c.txLock.Unlock()

	return c.graphDataTx.Items(), c.graphDataTxTime.Items()
}

func (c *chart) stop() {
	c.controller.Close()
	c.t.Close()
}

func (c *chart) termdashErrorHandler(err error) {
	if err != nil {
		panic(err)
	}
}

func timeToMapForSeriesXLabels(t []time.Time) map[int]string {
	m := make(map[int]string)
	for i, v := range t {
		if v.IsZero() {
			m[i] = " "
		} else {
			m[i] = v.Format("15:04:05.000")
		}
	}
	return m
}

type ringBuffer[T any] struct {
	pos   int
	items []T
	cap   int
}

func newRingBuffer[T any](size int) *ringBuffer[T] {
	if size <= 0 {
		panic(fmt.Sprintf("invalid size %d", size))
	}

	return &ringBuffer[T]{
		items: make([]T, 0, size),
		cap:   size,
	}
}

func (r *ringBuffer[T]) Add(item T) {
	if r.pos >= len(r.items) {
		r.items = append(r.items, item)
	} else {
		r.items[r.pos] = item
	}
	r.pos = (r.pos + 1) % cap(r.items)
}

func (r *ringBuffer[T]) Len() int {
	return len(r.items)
}

func (r *ringBuffer[T]) Items() []T {
	return append(r.items[r.pos:], r.items[:r.pos]...)
}
