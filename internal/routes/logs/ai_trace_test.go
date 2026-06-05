package logs

import (
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// statsCtx builds a gin.Context whose request carries the given query string.
func statsCtx(rawQuery string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/?"+rawQuery, nil)

	return c
}

func TestStatsWindow_ExplicitStartEnd(t *testing.T) {
	q := url.Values{}
	q.Set("start", "2026-03-01T08:30:00Z")
	q.Set("end", "2026-03-05T23:00:00Z")

	startDay, endDay, ok := statsWindow(statsCtx(q.Encode()))

	assert.True(t, ok)
	// Both bounds truncate to their UTC day.
	assert.Equal(t, "2026-03-01", startDay.Format("2006-01-02"))
	assert.Equal(t, "2026-03-05", endDay.Format("2006-01-02"))
	assert.Equal(t, time.UTC, startDay.Location())
}

func TestStatsWindow_EndBeforeStartRejected(t *testing.T) {
	q := url.Values{}
	q.Set("start", "2026-03-10T00:00:00Z")
	q.Set("end", "2026-03-01T00:00:00Z")

	_, _, ok := statsWindow(statsCtx(q.Encode()))
	assert.False(t, ok)
}

func TestStatsWindow_MalformedRejected(t *testing.T) {
	_, _, ok := statsWindow(statsCtx("start=not-a-time"))
	assert.False(t, ok)
}

func TestStatsWindow_ClampedToMaxDays(t *testing.T) {
	q := url.Values{}
	q.Set("start", "2026-01-01T00:00:00Z")
	q.Set("end", "2026-06-01T00:00:00Z") // far more than maxStatsDays apart

	startDay, endDay, ok := statsWindow(statsCtx(q.Encode()))

	assert.True(t, ok)
	// Span clamped to maxStatsDays buckets, anchored at the most recent end.
	bucketCount := int(endDay.Sub(startDay).Hours()/24) + 1
	assert.Equal(t, maxStatsDays, bucketCount)
	assert.Equal(t, "2026-06-01", endDay.Format("2006-01-02"))
}

func TestStatsWindow_DaysFallback(t *testing.T) {
	startDay, endDay, ok := statsWindow(statsCtx("days=30"))

	assert.True(t, ok)
	bucketCount := int(endDay.Sub(startDay).Hours()/24) + 1
	assert.Equal(t, 30, bucketCount)
	// endDay is today's UTC bucket.
	assert.Equal(t, time.Now().UTC().Truncate(24*time.Hour).Format("2006-01-02"), endDay.Format("2006-01-02"))
}

func TestStatsWindow_DefaultSevenDays(t *testing.T) {
	startDay, endDay, ok := statsWindow(statsCtx(""))

	assert.True(t, ok)
	bucketCount := int(endDay.Sub(startDay).Hours()/24) + 1
	assert.Equal(t, 7, bucketCount)
}

func TestStatsWindow_DaysOutOfRangeFallsBackToDefault(t *testing.T) {
	// >maxStatsDays is ignored, falling back to the 7-day default.
	startDay, endDay, ok := statsWindow(statsCtx("days=999"))

	assert.True(t, ok)
	bucketCount := int(endDay.Sub(startDay).Hours()/24) + 1
	assert.Equal(t, 7, bucketCount)
}
