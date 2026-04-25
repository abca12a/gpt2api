package image

import (
	"container/list"
	"runtime"
	"strings"
	"sync"
)

// Upscale 档位 —— 对外只暴露三档:空字符串 / "2k" / "4k"。
//
// 采用"长边目标像素"策略(避免不同比例出图的长边被裁),短边按原比例等比缩放:
//   - 2K:长边 2560
//   - 4K:长边 3840
const (
	UpscaleNone = ""
	Upscale2K   = "2k"
	Upscale4K   = "4k"
)

// ValidateUpscale 规整前端 / 上游传入的档位字符串,非法值一律视为空(原图)。
func ValidateUpscale(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case Upscale2K, Upscale4K:
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return UpscaleNone
	}
}

// UpscaleTargetLongSide 返回档位对应的"长边目标像素"。0 表示不放大。
func UpscaleTargetLongSide(scale string) int { return longSideOf(scale) }

func longSideOf(scale string) int {
	switch scale {
	case Upscale2K:
		return 2560
	case Upscale4K:
		return 3840
	default:
		return 0
	}
}

// ---------------- 并发闸 + LRU 缓存 ----------------

// UpscaleCache 进程内 LRU 字节缓存,附带一个并发信号量限制同时计算的数量。
//
// 设计动机:
//   - 同一张图第一次按 scale=4k 请求时需要调用外部超分服务;
//     命中缓存后毫秒级返回,交互体验差异巨大。
//   - 并发闸避免 4K 请求风暴打满外部 API 并发,影响生图主流程。
type UpscaleCache struct {
	mu       sync.Mutex
	items    map[string]*list.Element
	order    *list.List
	maxBytes int64
	curBytes int64

	sem     chan struct{}
	flights map[string]*upscaleFlight
}

type upscaleEntry struct {
	key         string
	data        []byte
	contentType string
}

type upscaleFlight struct {
	done        chan struct{}
	data        []byte
	contentType string
	noop        bool
	err         error
}

// NewUpscaleCache 初始化 LRU。maxBytes ≤ 0 时使用默认 512MB;并发上限默认 NumCPU。
//
// 默认 512MB 足够放 ~50 张 4K PNG,对面板"刚生成 + 回头再看"的场景命中率很高。
func NewUpscaleCache(maxBytes int64, concurrency int) *UpscaleCache {
	if maxBytes <= 0 {
		maxBytes = 512 * 1024 * 1024
	}
	if concurrency <= 0 {
		concurrency = runtime.NumCPU()
		if concurrency < 2 {
			concurrency = 2
		}
	}
	return &UpscaleCache{
		items:    make(map[string]*list.Element),
		order:    list.New(),
		maxBytes: maxBytes,
		sem:      make(chan struct{}, concurrency),
		flights:  make(map[string]*upscaleFlight),
	}
}

// Get 命中时返回 (data, ct, true);未命中返回 false。命中会把条目移到 LRU 尾(最新)。
func (c *UpscaleCache) Get(key string) ([]byte, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, "", false
	}
	c.order.MoveToBack(el)
	e := el.Value.(*upscaleEntry)
	return e.data, e.contentType, true
}

// Put 写入缓存,超过容量时从头(最老)淘汰直到合规。
func (c *UpscaleCache) Put(key string, data []byte, contentType string) {
	if len(data) == 0 || int64(len(data)) > c.maxBytes {
		return // 单条就能撑爆缓存的直接放弃
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		old := el.Value.(*upscaleEntry)
		c.curBytes -= int64(len(old.data))
		old.data = data
		old.contentType = contentType
		c.curBytes += int64(len(data))
		c.order.MoveToBack(el)
		return
	}
	e := &upscaleEntry{key: key, data: data, contentType: contentType}
	el := c.order.PushBack(e)
	c.items[key] = el
	c.curBytes += int64(len(data))
	for c.curBytes > c.maxBytes {
		front := c.order.Front()
		if front == nil {
			break
		}
		old := front.Value.(*upscaleEntry)
		c.order.Remove(front)
		delete(c.items, old.key)
		c.curBytes -= int64(len(old.data))
	}
}

// Do 对同一个 key 合并并发计算,避免多次请求同一张图时重复调用外部超分服务。
func (c *UpscaleCache) Do(key string, fn func() ([]byte, string, bool, error)) ([]byte, string, bool, error, bool) {
	c.mu.Lock()
	if f, ok := c.flights[key]; ok {
		c.mu.Unlock()
		<-f.done
		return f.data, f.contentType, f.noop, f.err, true
	}
	f := &upscaleFlight{done: make(chan struct{})}
	c.flights[key] = f
	c.mu.Unlock()

	f.data, f.contentType, f.noop, f.err = fn()

	c.mu.Lock()
	delete(c.flights, key)
	c.mu.Unlock()
	close(f.done)
	return f.data, f.contentType, f.noop, f.err, false
}

// Acquire 占用一格并发配额;请与 Release 成对使用。
func (c *UpscaleCache) Acquire() { c.sem <- struct{}{} }

// Release 释放一格。
func (c *UpscaleCache) Release() { <-c.sem }
