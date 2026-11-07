package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type catalogChannelRepo struct {
	service.ChannelRepository
	channel service.Channel
}

func (r *catalogChannelRepo) ListAll(context.Context) ([]service.Channel, error) {
	return []service.Channel{r.channel}, nil
}

func (r *catalogChannelRepo) GetGroupPlatforms(context.Context, []int64) (map[int64]string, error) {
	return map[int64]string{10: service.PlatformOpenAI}, nil
}

func TestGatewayModels_ChannelMappingListsPublicModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	group := &service.Group{ID: 10, Platform: service.PlatformOpenAI, ModelAllowlist: service.GroupModelAllowlist{
		Enabled: true, Models: []string{"codex-auto-review"},
	}}
	mapping := make(map[string]string)
	for i := 1; i <= 6; i++ {
		model := fmt.Sprintf("public-model-%d", i)
		group.ModelAllowlist.Models = append(group.ModelAllowlist.Models, model)
		mapping[model] = "gpt-6-luna"
	}
	channels := service.NewChannelService(&catalogChannelRepo{channel: service.Channel{
		ID: 1, Status: service.StatusActive, GroupIDs: []int64{group.ID},
		ModelMapping: map[string]map[string]string{service.PlatformOpenAI: mapping},
	}}, nil, nil, nil, nil)
	repo := &gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{group.ID: {{
		ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"model_mapping": map[string]any{
			"codex-auto-review": "codex-auto-review", "gpt-6-luna": "gpt-6-luna",
		}},
	}}}}
	h := &GatewayHandler{gatewayService: service.NewGatewayService(
		repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, channels, nil, nil, nil, nil,
	)}
	for _, path := range []string{"/v1/models", "/v1/models/public-model-1", "/v1/models/gpt-6-luna"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, path, nil)
			if path != "/v1/models" {
				c.Params = gin.Params{{Key: "model", Value: strings.TrimPrefix(path, "/v1/models/")}}
			}
			c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{Group: group, GroupID: &group.ID})
			h.Models(c)
			if path == "/v1/models/gpt-6-luna" {
				require.Equal(t, http.StatusNotFound, rec.Code)
				return
			}
			require.Equal(t, http.StatusOK, rec.Code)
			if path != "/v1/models" {
				var got gatewayModelItemForTest
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
				require.Equal(t, "public-model-1", got.ID)
				return
			}
			var got gatewayModelsResponseForTest
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			require.Equal(t, group.ModelAllowlist.Models, modelIDsForTest(got.Data))
		})
	}
}
