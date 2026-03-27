package driven

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSystemClockNowIsApproximatelyCurrentTime(t *testing.T) {
	clk := SystemClock{}
	before := time.Now()
	got := clk.Now()
	after := time.Now()

	assert.False(t, got.Before(before), "Now must not return a time before the call")
	assert.False(t, got.After(after), "Now must not return a time after the call")
}
