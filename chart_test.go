package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRingBuffer(t *testing.T) {
	r := newRingBuffer[int](10)
	require.NotNil(t, r)
	require.Equal(t, 0, r.Len())

	r.Add(1)
	require.Equal(t, 1, r.Len())
	require.Equal(t, []int{1}, r.Items())

	for i := 2; i <= 10; i++ {
		r.Add(i)
	}
	require.Equal(t, 10, r.Len())
	require.Equal(t, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, r.Items())

	r.Add(11)
	require.Equal(t, 10, r.Len())
	require.Equal(t, []int{2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, r.Items())

	for i := 12; i <= 20; i++ {
		r.Add(i)
	}
	require.Equal(t, 10, r.Len())
	require.Equal(t, []int{11, 12, 13, 14, 15, 16, 17, 18, 19, 20}, r.Items())
}
