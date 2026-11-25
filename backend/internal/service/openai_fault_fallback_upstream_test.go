package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// faultFallbackRepoStub 只实现本测试用到的 FindByExtraField。
// 其余 AccountRepository 方法由嵌入的 nil 接口兜底（被调用即 panic，便于定位）。
type faultFallbackRepoStub struct {
	AccountRepository
	mu       sync.Mutex
	accounts []Account
	err      error
	calls    int
	lastKey  string
}

func (s *faultFallbackRepoStub) FindByExtraField(_ context.Context, key string, _ any) ([]Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.lastKey = key
	return s.accounts, s.err
}

func (s *faultFallbackRepoStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func faultFallbackAccount(extra map[string]any) *Account {
	return &Account{
		ID:       84,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra:    extra,
	}
}

// TestOpenAIFaultFallbackTriggerMatches 覆盖兜底账号条件匹配的判定边界。
func TestOpenAIFaultFallbackTriggerMatches(t *testing.T) {
	tests := []struct {
		name     string
		account  *Account
		status   int
		message  string
		expected bool
	}{
		{
			name: "all 模式：状态码与关键词都命中",
			account: faultFallbackAccount(map[string]any{
				OpenAIFaultFallbackOnlyExtraKey:        true,
				OpenAIFaultFallbackStatusCodesExtraKey: []any{400, 403},
				OpenAIFaultFallbackKeywordsExtraKey:    []any{"请求被拒绝"},
				OpenAIFaultFallbackMatchModeExtraKey:   "all",
			}),
			status:   400,
			message:  "请求被拒绝",
			expected: true,
		},
		{
			name: "all 模式：关键词未命中",
			account: faultFallbackAccount(map[string]any{
				OpenAIFaultFallbackOnlyExtraKey:        true,
				OpenAIFaultFallbackStatusCodesExtraKey: []any{400, 403},
				OpenAIFaultFallbackKeywordsExtraKey:    []any{"请求被拒绝"},
				OpenAIFaultFallbackMatchModeExtraKey:   "all",
			}),
			status:   400,
			message:  "quota exceeded",
			expected: false,
		},
		{
			name: "all 模式：状态码未命中",
			account: faultFallbackAccount(map[string]any{
				OpenAIFaultFallbackOnlyExtraKey:        true,
				OpenAIFaultFallbackStatusCodesExtraKey: []any{400, 403},
				OpenAIFaultFallbackKeywordsExtraKey:    []any{"请求被拒绝"},
				OpenAIFaultFallbackMatchModeExtraKey:   "all",
			}),
			status:   404,
			message:  "请求被拒绝",
			expected: false,
		},
		{
			name: "持久化的 float64 状态码可解析",
			account: faultFallbackAccount(map[string]any{
				OpenAIFaultFallbackOnlyExtraKey:        true,
				OpenAIFaultFallbackStatusCodesExtraKey: []any{float64(400), float64(403)},
				OpenAIFaultFallbackKeywordsExtraKey:    []any{"请求被拒绝"},
				OpenAIFaultFallbackMatchModeExtraKey:   "all",
			}),
			status:   400,
			message:  "请求被拒绝",
			expected: true,
		},
		{
			name: "any 模式：仅状态码命中",
			account: faultFallbackAccount(map[string]any{
				OpenAIFaultFallbackOnlyExtraKey:        true,
				OpenAIFaultFallbackStatusCodesExtraKey: []any{400},
				OpenAIFaultFallbackKeywordsExtraKey:    []any{"请求被拒绝"},
				OpenAIFaultFallbackMatchModeExtraKey:   "any",
			}),
			status:   400,
			message:  "",
			expected: true,
		},
		{
			name: "any 模式：都不命中",
			account: faultFallbackAccount(map[string]any{
				OpenAIFaultFallbackOnlyExtraKey:        true,
				OpenAIFaultFallbackStatusCodesExtraKey: []any{400},
				OpenAIFaultFallbackKeywordsExtraKey:    []any{"请求被拒绝"},
				OpenAIFaultFallbackMatchModeExtraKey:   "any",
			}),
			status:   404,
			message:  "quota exceeded",
			expected: false,
		},
		{
			name: "未配置状态码时回退默认值：429 命中",
			account: faultFallbackAccount(map[string]any{
				OpenAIFaultFallbackOnlyExtraKey:     true,
				OpenAIFaultFallbackKeywordsExtraKey: []any{"请求被拒绝"},
			}),
			status:   429,
			message:  "",
			expected: true,
		},
		{
			name: "未配置状态码时默认值不含 400",
			account: faultFallbackAccount(map[string]any{
				OpenAIFaultFallbackOnlyExtraKey:     true,
				OpenAIFaultFallbackKeywordsExtraKey: []any{"请求被拒绝"},
			}),
			status:   400,
			message:  "",
			expected: false,
		},
		{
			name:     "nil 账号",
			account:  nil,
			status:   400,
			message:  "请求被拒绝",
			expected: false,
		},
		{
			name: "非兜底账号",
			account: faultFallbackAccount(map[string]any{
				OpenAIFaultFallbackStatusCodesExtraKey: []any{400},
				OpenAIFaultFallbackKeywordsExtraKey:    []any{"请求被拒绝"},
			}),
			status:   400,
			message:  "请求被拒绝",
			expected: false,
		},
		{
			name: "配置损坏时 fail closed",
			account: faultFallbackAccount(map[string]any{
				OpenAIFaultFallbackOnlyExtraKey:     true,
				OpenAIFaultFallbackKeywordsExtraKey: "request rejected",
			}),
			status:   400,
			message:  "request rejected",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, openAIFaultFallbackTriggerMatches(tt.account, tt.status, tt.message))
		})
	}
}

// TestShouldFailoverOpenAIFaultFallback400 验证：只有存在匹配的兜底账号时，
// 400 才会从「不可重放的客户端错误」升级为可换号错误。
func TestShouldFailoverOpenAIFaultFallback400(t *testing.T) {
	source := &Account{ID: 83, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"error":{"message":"请求被拒绝","type":"invalid_request_error"}}`)

	matching := Account{
		ID:       84,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra: map[string]any{
			OpenAIFaultFallbackOnlyExtraKey:        true,
			OpenAIFaultFallbackStatusCodesExtraKey: []any{400, 403},
			OpenAIFaultFallbackKeywordsExtraKey:    []any{"请求被拒绝"},
			OpenAIFaultFallbackMatchModeExtraKey:   "all",
		},
	}
	nonMatching := matching
	nonMatching.ID = 85
	nonMatching.Extra = map[string]any{
		OpenAIFaultFallbackOnlyExtraKey:        true,
		OpenAIFaultFallbackStatusCodesExtraKey: []any{400},
		OpenAIFaultFallbackKeywordsExtraKey:    []any{"insufficient balance"},
		OpenAIFaultFallbackMatchModeExtraKey:   "all",
	}

	t.Run("基线：无兜底账号时 400 不换号", func(t *testing.T) {
		svc := &OpenAIGatewayService{accountRepo: &faultFallbackRepoStub{}}
		require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(source, http.StatusBadRequest, "请求被拒绝", body))
		require.False(t, svc.shouldFailoverOpenAIUpstreamResponseWithFaultFallback(
			context.Background(), source, http.StatusBadRequest, "请求被拒绝", body))
	})

	t.Run("存在匹配的兜底账号时 400 换号", func(t *testing.T) {
		repo := &faultFallbackRepoStub{accounts: []Account{matching}}
		svc := &OpenAIGatewayService{accountRepo: repo}
		require.True(t, svc.shouldFailoverOpenAIUpstreamResponseWithFaultFallback(
			context.Background(), source, http.StatusBadRequest, "请求被拒绝", body))
		require.Equal(t, 1, repo.callCount())
		require.Equal(t, OpenAIFaultFallbackOnlyExtraKey, repo.lastKey)
	})

	t.Run("兜底账号条件不匹配时 400 不换号", func(t *testing.T) {
		svc := &OpenAIGatewayService{accountRepo: &faultFallbackRepoStub{accounts: []Account{nonMatching}}}
		require.False(t, svc.shouldFailoverOpenAIUpstreamResponseWithFaultFallback(
			context.Background(), source, http.StatusBadRequest, "请求被拒绝", body))
	})

	t.Run("候选列表查询失败时 fail closed", func(t *testing.T) {
		svc := &OpenAIGatewayService{accountRepo: &faultFallbackRepoStub{err: errors.New("db down")}}
		require.False(t, svc.shouldFailoverOpenAIUpstreamResponseWithFaultFallback(
			context.Background(), source, http.StatusBadRequest, "请求被拒绝", body))
	})

	t.Run("cyber_policy 400 即使有兜底账号也不换号", func(t *testing.T) {
		cyber := []byte(`{"error":{"code":"cyber_policy","message":"请求被拒绝"}}`)
		svc := &OpenAIGatewayService{accountRepo: &faultFallbackRepoStub{accounts: []Account{matching}}}
		require.False(t, svc.shouldFailoverOpenAIUpstreamResponseWithFaultFallback(
			context.Background(), source, http.StatusBadRequest, "请求被拒绝", cyber))
	})

	t.Run("5xx 沿用原有判定且不查兜底账号", func(t *testing.T) {
		repo := &faultFallbackRepoStub{}
		svc := &OpenAIGatewayService{accountRepo: repo}
		require.True(t, svc.shouldFailoverOpenAIUpstreamResponseWithFaultFallback(
			context.Background(), source, http.StatusInternalServerError, "boom", []byte(`{}`)))
		require.Zero(t, repo.callCount())
	})

	t.Run("没有仓储时 400 不换号", func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		require.False(t, svc.shouldFailoverOpenAIUpstreamResponseWithFaultFallback(
			context.Background(), source, http.StatusBadRequest, "请求被拒绝", body))
	})
}

// TestShouldFailoverOpenAIPassthroughFallback400 验证透传路径与主转发路径行为一致：
// 透传账号不经过 forward() 的 400 兜底升级，必须在自己的错误分支里补同样的判定，
// 否则兜底账号对开启了「自动透传」的账号永远不生效。
func TestShouldFailoverOpenAIPassthroughFallback400(t *testing.T) {
	// 生产形态：账号 #66 开启 openai_passthrough，上游要求流式请求。
	source := &Account{
		ID:       66,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra:    map[string]any{"openai_passthrough": true},
	}
	body := []byte(`{"error":{"code":"stream_required","message":"This group only supports streaming generation requests. Set stream to true.","param":"stream","type":"invalid_request_error"}}`)
	msg := "This group only supports streaming generation requests. Set stream to true."

	matching := Account{
		ID:       88,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra: map[string]any{
			OpenAIFaultFallbackOnlyExtraKey:        true,
			OpenAIFaultFallbackStatusCodesExtraKey: []any{400},
			OpenAIFaultFallbackKeywordsExtraKey:    []any{"This group only supports streaming"},
			OpenAIFaultFallbackMatchModeExtraKey:   "all",
		},
	}
	nonMatching := matching
	nonMatching.ID = 89
	nonMatching.Extra = map[string]any{
		OpenAIFaultFallbackOnlyExtraKey:        true,
		OpenAIFaultFallbackStatusCodesExtraKey: []any{400},
		OpenAIFaultFallbackKeywordsExtraKey:    []any{"insufficient balance"},
		OpenAIFaultFallbackMatchModeExtraKey:   "all",
	}

	t.Run("存在匹配兜底账号时透传 400 也会换号", func(t *testing.T) {
		repo := &faultFallbackRepoStub{accounts: []Account{matching}}
		svc := &OpenAIGatewayService{accountRepo: repo}
		require.True(t, svc.shouldFailoverOpenAIPassthroughResponseWithFaultFallback(
			context.Background(), source, http.StatusBadRequest, msg, body))
		require.Equal(t, 1, repo.callCount())
	})

	t.Run("没有兜底账号时保持原样透传 400", func(t *testing.T) {
		repo := &faultFallbackRepoStub{}
		svc := &OpenAIGatewayService{accountRepo: repo}
		require.False(t, svc.shouldFailoverOpenAIPassthroughResponseWithFaultFallback(
			context.Background(), source, http.StatusBadRequest, msg, body))
	})

	t.Run("兜底账号条件不匹配时不换号", func(t *testing.T) {
		svc := &OpenAIGatewayService{accountRepo: &faultFallbackRepoStub{accounts: []Account{nonMatching}}}
		require.False(t, svc.shouldFailoverOpenAIPassthroughResponseWithFaultFallback(
			context.Background(), source, http.StatusBadRequest, msg, body))
	})

	t.Run("候选查询失败时 fail closed", func(t *testing.T) {
		svc := &OpenAIGatewayService{accountRepo: &faultFallbackRepoStub{err: errors.New("db down")}}
		require.False(t, svc.shouldFailoverOpenAIPassthroughResponseWithFaultFallback(
			context.Background(), source, http.StatusBadRequest, msg, body))
	})

	t.Run("cyber_policy 400 不因兜底账号而换号", func(t *testing.T) {
		cyber := []byte(`{"error":{"code":"cyber_policy","message":"This group only supports streaming"}}`)
		svc := &OpenAIGatewayService{accountRepo: &faultFallbackRepoStub{accounts: []Account{matching}}}
		require.False(t, svc.shouldFailoverOpenAIPassthroughResponseWithFaultFallback(
			context.Background(), source, http.StatusBadRequest, "This group only supports streaming", cyber))
	})

	t.Run("5xx 沿用透传原有判定且不查兜底账号", func(t *testing.T) {
		repo := &faultFallbackRepoStub{}
		svc := &OpenAIGatewayService{accountRepo: repo}
		require.True(t, svc.shouldFailoverOpenAIPassthroughResponseWithFaultFallback(
			context.Background(), source, http.StatusBadGateway, "boom", []byte(`{}`)))
		require.Zero(t, repo.callCount())
	})

	t.Run("没有仓储时透传 400 不换号", func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		require.False(t, svc.shouldFailoverOpenAIPassthroughResponseWithFaultFallback(
			context.Background(), source, http.StatusBadRequest, msg, body))
	})
}

// TestOpenAIFaultFallbackCandidateCache 验证进程内候选缓存的命中、过期与并发行为。
func TestOpenAIFaultFallbackCandidateCache(t *testing.T) {
	candidate := Account{
		ID:       84,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra: map[string]any{
			OpenAIFaultFallbackOnlyExtraKey:        true,
			OpenAIFaultFallbackStatusCodesExtraKey: []any{400, 403},
			OpenAIFaultFallbackKeywordsExtraKey:    []any{"请求被拒绝"},
			OpenAIFaultFallbackMatchModeExtraKey:   "all",
		},
	}

	t.Run("TTL 内复用，过期后重新查询", func(t *testing.T) {
		repo := &faultFallbackRepoStub{accounts: []Account{candidate}}
		svc := &OpenAIGatewayService{accountRepo: repo}

		require.Len(t, svc.openAIFaultFallbackCandidates(context.Background()), 1)
		require.Equal(t, 1, repo.callCount())

		require.Len(t, svc.openAIFaultFallbackCandidates(context.Background()), 1)
		require.Equal(t, 1, repo.callCount(), "TTL 内不应再次查库")

		svc.faultFallbackCandidates.store(svc.faultFallbackCandidates.candidates, time.Now().Add(-time.Second))
		require.Len(t, svc.openAIFaultFallbackCandidates(context.Background()), 1)
		require.Equal(t, 2, repo.callCount(), "过期后应重新查库")
	})

	t.Run("空结果也缓存", func(t *testing.T) {
		repo := &faultFallbackRepoStub{}
		svc := &OpenAIGatewayService{accountRepo: repo}
		require.Empty(t, svc.openAIFaultFallbackCandidates(context.Background()))
		require.Empty(t, svc.openAIFaultFallbackCandidates(context.Background()))
		require.Equal(t, 1, repo.callCount(), "没有候选时也要缓存，避免每次都查库")
	})

	t.Run("查询失败不缓存", func(t *testing.T) {
		repo := &faultFallbackRepoStub{err: errors.New("db down")}
		svc := &OpenAIGatewayService{accountRepo: repo}
		require.Empty(t, svc.openAIFaultFallbackCandidates(context.Background()))
		require.Empty(t, svc.openAIFaultFallbackCandidates(context.Background()))
		require.Equal(t, 2, repo.callCount(), "失败结果不能缓存，否则一次抖动会锁死 30 秒")
	})

	t.Run("并发冷启动只查一次", func(t *testing.T) {
		repo := &faultFallbackRepoStub{accounts: []Account{candidate}}
		svc := &OpenAIGatewayService{accountRepo: repo}
		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = svc.openAIFaultFallbackCandidates(context.Background())
			}()
		}
		wg.Wait()
		require.Equal(t, 1, repo.callCount(), "并发冷启动应由 singleflight 收敛为一次查询")
	})

	t.Run("TTL 内不同消息仍各自判定", func(t *testing.T) {
		repo := &faultFallbackRepoStub{accounts: []Account{candidate}}
		svc := &OpenAIGatewayService{accountRepo: repo}
		source := &Account{ID: 83, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

		require.True(t, svc.shouldFailoverOpenAIUpstreamResponseWithFaultFallback(
			context.Background(), source, http.StatusBadRequest, "请求被拒绝",
			[]byte(`{"error":{"message":"请求被拒绝"}}`)))
		require.False(t, svc.shouldFailoverOpenAIUpstreamResponseWithFaultFallback(
			context.Background(), source, http.StatusBadRequest, "quota exceeded",
			[]byte(`{"error":{"message":"quota exceeded"}}`)),
			"缓存的是候选条件，不是判定结果：另一种消息不该继承命中")
		require.Equal(t, 1, repo.callCount(), "两次判定共享同一份候选缓存")
	})
}
