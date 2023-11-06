package controller

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"mime/multipart"
	"net/http"
	"one-api/common"
	"one-api/model"
	"strconv"
	"strings"
)

type Message struct {
	Role    string  `json:"role"`
	Content string  `json:"content"`
	Name    *string `json:"name,omitempty"`
}

const (
	RelayModeUnknown = iota
	RelayModeChatCompletions
	RelayModeCompletions
	RelayModeEmbeddings
	RelayModeModerations
	RelayModeImagesGenerations
	RelayModeEdits
)

// https://platform.openai.com/docs/api-reference/chat

type GeneralOpenAIRequest struct {
	Model       string    `json:"model,omitempty"`
	Messages    []Message `json:"messages,omitempty"`
	Prompt      any       `json:"prompt,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	TopP        float64   `json:"top_p,omitempty"`
	N           int       `json:"n,omitempty"`
	Input       any       `json:"input,omitempty"`
	Instruction string    `json:"instruction,omitempty"`
	Size        string    `json:"size,omitempty"`
}

type ChatRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens"`
}

type TextRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	Prompt    string    `json:"prompt"`
	MaxTokens int       `json:"max_tokens"`
	//Stream   bool      `json:"stream"`
}

type ImageRequest struct {
	Prompt string `json:"prompt"`
	N      int    `json:"n"`
	Size   string `json:"size"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type OpenAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param"`
	Code    any    `json:"code"`
}

type OpenAIErrorWithStatusCode struct {
	OpenAIError
	StatusCode int `json:"status_code"`
}

type TextResponse struct {
	Usage `json:"usage"`
	Error OpenAIError `json:"error"`
}

type OpenAITextResponseChoice struct {
	Index        int `json:"index"`
	Message      `json:"message"`
	FinishReason string `json:"finish_reason"`
}

type OpenAITextResponse struct {
	Id      string                     `json:"id"`
	Object  string                     `json:"object"`
	Created int64                      `json:"created"`
	Choices []OpenAITextResponseChoice `json:"choices"`
	Usage   `json:"usage"`
}

type OpenAIEmbeddingResponseItem struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

type OpenAIEmbeddingResponse struct {
	Object string                        `json:"object"`
	Data   []OpenAIEmbeddingResponseItem `json:"data"`
	Model  string                        `json:"model"`
	Usage  `json:"usage"`
}

type ImageResponse struct {
	Created int `json:"created"`
	Data    []struct {
		Url string `json:"url"`
	}
}

type ChatCompletionsStreamResponseChoice struct {
	Delta struct {
		Content string `json:"content"`
	} `json:"delta"`
	FinishReason string `json:"finish_reason,omitempty"`
}

type ChatCompletionsStreamResponse struct {
	Id      string                                `json:"id"`
	Object  string                                `json:"object"`
	Created int64                                 `json:"created"`
	Model   string                                `json:"model"`
	Choices []ChatCompletionsStreamResponseChoice `json:"choices"`
}

type CompletionsStreamResponse struct {
	Choices []struct {
		Text         string `json:"text"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

type AudioTranscriptionsRequest struct {
	File           *multipart.FileHeader `form:"file" binding:"required"`
	Model          string                `form:"model" binding:"required"`
	Prompt         string                `form:"prompt"`
	ResponseFormat string                `form:"response_format"`
	Temperature    float64               `form:"temperature"`
	Language       string                `form:"language"`
}

func Relay(c *gin.Context) {
	relayMode := RelayModeUnknown
	if strings.HasPrefix(c.Request.URL.Path, "/v1/chat/completions") {
		relayMode = RelayModeChatCompletions
	} else if strings.HasPrefix(c.Request.URL.Path, "/v1/completions") {
		relayMode = RelayModeCompletions
	} else if strings.HasPrefix(c.Request.URL.Path, "/v1/embeddings") {
		relayMode = RelayModeEmbeddings
	} else if strings.HasSuffix(c.Request.URL.Path, "embeddings") {
		relayMode = RelayModeEmbeddings
	} else if strings.HasPrefix(c.Request.URL.Path, "/v1/moderations") {
		relayMode = RelayModeModerations
	} else if strings.HasPrefix(c.Request.URL.Path, "/v1/images/generations") {
		relayMode = RelayModeImagesGenerations
	} else if strings.HasPrefix(c.Request.URL.Path, "/v1/edits") {
		relayMode = RelayModeEdits
	}
	var err *OpenAIErrorWithStatusCode
	switch relayMode {
	case RelayModeImagesGenerations:
		err = relayImageHelper(c, relayMode)
	default:
		err = relayTextHelper(c, relayMode)
	}
	if err != nil {
		retryTimesStr := c.Query("retry")
		retryTimes, _ := strconv.Atoi(retryTimesStr)
		if retryTimesStr == "" {
			retryTimes = common.RetryTimes
		}
		if retryTimes > 0 {
			c.Redirect(http.StatusTemporaryRedirect, fmt.Sprintf("%s?retry=%d", c.Request.URL.Path, retryTimes-1))
		} else {
			if err.StatusCode == http.StatusTooManyRequests {
				err.OpenAIError.Message = "当前分组负载已饱和，请稍后再试，或升级账户以提升服务质量。"
			}
			c.JSON(err.StatusCode, gin.H{
				"error": err.OpenAIError,
			})
		}
		channelId := c.GetInt("channel_id")
		common.SysError(fmt.Sprintf("relay error (channel #%d): %s", channelId, err.Message))
		// https://platform.openai.com/docs/guides/error-codes/api-errors
		if shouldDisableChannel(&err.OpenAIError) {
			channelId := c.GetInt("channel_id")
			channelName := c.GetString("channel_name")
			disableChannel(channelId, channelName, err.Message)
		}
	}
}

func RelayAudio(c *gin.Context) {
	var form AudioTranscriptionsRequest
	// 在这种情况下，将自动选择合适的绑定
	if c.ShouldBind(&form) != nil {
		err := OpenAIError{
			Message: "bind_form_failed",
			Type:    "one_api_error",
			Param:   "",
			Code:    "audio_error",
		}
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err,
		})
		return
	}
	file, err := form.File.Open()
	if err != nil {
		err := OpenAIError{
			Message: "Open file error",
			Type:    "one_api_error",
			Param:   "",
			Code:    "audio_error",
		}
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err,
		})
		return
	}
	defer file.Close()
	var header common.WAVHeader
	err = binary.Read(file, binary.LittleEndian, &header)
	if err != nil {
		err := OpenAIError{
			Message: "Read file error",
			Type:    "one_api_error",
			Param:   "",
			Code:    "audio_error",
		}
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err,
		})
		return
	}

	var duration float64
	if !bytes.Equal(header.RIFF[:], []byte("RIFF")) || !bytes.Equal(header.WAVE[:], []byte("WAVE")) {
		// 如果不能解析为wav文件，则使用默认配置
		// wav 一般Byte Rate为:16000
		duration = float64(form.File.Size) / 16000
	} else {
		duration = float64(header.DataSize) / float64(header.ByteRate)
	}

	channelType := c.GetInt("channel")
	baseURL := common.ChannelBaseURLs[channelType]
	requestURL := c.Request.URL.Path
	if c.GetString("base_url") != "" {
		baseURL = c.GetString("base_url")
	}
	fullRequestURL := fmt.Sprintf("%s%s", baseURL, requestURL)

	// 创建一个缓冲区，用于存储请求体
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// 创建multipart/form-data的一部分，其中包含文件内容
	part, err := writer.CreateFormFile("file", form.File.Filename)
	if err != nil {
		err := OpenAIError{
			Message: "create_form_file_failed",
			Type:    "one_api_error",
			Param:   "",
			Code:    "audio_error",
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err,
		})
		return
	}
	// 将文件内容拷贝到 multipart writer
	// 重置文件读取偏移量
	_, _ = file.Seek(0, 0)
	_, err = io.Copy(part, file)
	_ = writer.WriteField("model", form.Model)
	_ = writer.WriteField("prompt", form.Prompt)
	_ = writer.WriteField("response_format", form.ResponseFormat)
	_ = writer.WriteField("temperature", strconv.FormatFloat(form.Temperature, 'f', -1, 64))
	_ = writer.WriteField("language", form.Language)
	// 结束 multipart 写操作
	_ = writer.Close()
	req, err := http.NewRequest(c.Request.Method, fullRequestURL, body)
	if err != nil {
		err := OpenAIError{
			Message: "new_request_failed",
			Type:    "one_api_error",
			Param:   "",
			Code:    "audio_error",
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err,
		})
		return
	}

	req.Header.Set("Authorization", c.Request.Header.Get("Authorization"))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", c.Request.Header.Get("Accept"))

	//reqDump, err := httputil.DumpRequestOut(req, true)
	//fmt.Printf("REQUEST:\n%s", string(reqDump))

	resp, err := httpClient.Do(req)
	if err != nil {
		common.SysLog("RelayAudio do_request_failed " + err.Error())
		err := OpenAIError{
			Message: "do_request_failed",
			Type:    "one_api_error",
			Param:   err.Error(),
			Code:    "audio_error",
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err,
		})
		return
	}

	defer func() {
		if resp.StatusCode == 200 {
			// 计费
			QuotaPerUnit := common.QuotaPerUnit
			// 语音模型倍率(每分钟需要消耗的额度)
			m := QuotaPerUnit * 0.006
			// 每秒需要消耗的额度
			s := m / 60
			// 本次请求消耗的额度
			quota := int(duration * s)
			tokenId := c.GetInt("token_id")
			userId := c.GetInt("id")
			group := c.GetString("group")
			imageModel := "whisper-1"
			modelRatio := common.GetModelRatio(imageModel)
			groupRatio := common.GetGroupRatio(group)

			err := model.PostConsumeTokenQuota(tokenId, quota)
			if err != nil {
				common.SysError("error consuming token remain quota: " + err.Error())
			}
			err = model.CacheUpdateUserQuota(userId)
			if err != nil {
				common.SysError("error update user quota cache: " + err.Error())
			}
			if quota != 0 {
				tokenName := c.GetString("token_name")
				logContent := fmt.Sprintf("模型倍率 %.2f，分组倍率 %.2f", modelRatio, groupRatio)
				model.RecordConsumeLog(userId, 0, 0, imageModel, tokenName, quota, logContent)
				model.UpdateUserUsedQuotaAndRequestCount(userId, quota)
				channelId := c.GetInt("channel_id")
				model.UpdateChannelUsedQuota(channelId, quota)
			}
		}
	}()

	err = req.Body.Close()
	if err != nil {
		err := OpenAIError{
			Message: "close_request_body_failed",
			Type:    "one_api_error",
			Param:   "",
			Code:    "audio_error",
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err,
		})
		return
	}
	err = c.Request.Body.Close()
	if err != nil {
		err := OpenAIError{
			Message: "close_request_body_failed",
			Type:    "one_api_error",
			Param:   "",
			Code:    "audio_error",
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err,
		})
		return
	}

	for k, v := range resp.Header {
		c.Writer.Header().Set(k, v[0])
	}
	c.Writer.WriteHeader(resp.StatusCode)

	_, err = io.Copy(c.Writer, resp.Body)
	if err != nil {
		err := OpenAIError{
			Message: "copy_response_body_failed",
			Type:    "one_api_error",
			Param:   "",
			Code:    "audio_error",
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err,
		})
		return
	}
	err = resp.Body.Close()
	if err != nil {
		err := OpenAIError{
			Message: "close_response_body_failed",
			Type:    "one_api_error",
			Param:   "",
			Code:    "audio_error",
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err,
		})
		return
	}

}

func RelayNotImplemented(c *gin.Context) {
	err := OpenAIError{
		Message: "API not implemented",
		Type:    "one_api_error",
		Param:   "",
		Code:    "api_not_implemented",
	}
	c.JSON(http.StatusNotImplemented, gin.H{
		"error": err,
	})
}

func RelayNotFound(c *gin.Context) {
	err := OpenAIError{
		Message: fmt.Sprintf("API not found: %s:%s", c.Request.Method, c.Request.URL.Path),
		Type:    "one_api_error",
		Param:   "",
		Code:    "api_not_found",
	}
	c.JSON(http.StatusNotFound, gin.H{
		"error": err,
	})
}
