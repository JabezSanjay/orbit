package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// PublishLatency measures the time taken to publish a message safely to the global broker.
	PublishLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "orbit_publish_latency_seconds",
		Help:    "Latency of publishing messages to the broker.",
		Buckets: prometheus.DefBuckets,
	})

	// FanoutLatency measures the time taken for a received broker message to be actively written to a websocket buffer.
	FanoutLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "orbit_fanout_latency_seconds",
		Help:    "Latency of dispatching messages to client websockets.",
		Buckets: prometheus.DefBuckets,
	})

	// ActiveSubscriptions is a gauge tracking the number of active topics subscribed currently.
	ActiveSubscriptions = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "orbit_active_subscriptions",
		Help: "Total number of actively multiplexed channels.",
	})

	// ReconnectsTotal tracks how often the broker connection has successfully reinitialized itself.
	ReconnectsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "orbit_reconnects_total",
		Help: "Total number of broker reconnections due to network failures.",
	})

	// DroppedMessagesTotal tracks how many inbound messages were violently discarded due to internal backpressure.
	DroppedMessagesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "orbit_dropped_messages_total",
		Help: "Total number of messages dropped locally due to queue saturation.",
	})

	// InboundRateLimitedTotal counts client frames dropped by the per-connection inbound rate limit.
	InboundRateLimitedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "orbit_inbound_rate_limited_total",
		Help: "Total number of client frames dropped by the per-connection inbound rate limit.",
	})

	// ActiveConnections tracks the number of websockets connected locally.
	ActiveConnections = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "orbit_active_connections",
		Help: "Total number of active websocket connections on this node.",
	})
)
