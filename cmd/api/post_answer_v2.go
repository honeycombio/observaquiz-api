package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"observaquiz_lambda/cmd/api/deepchecks"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type openaiApi struct {
	model  string
	client *openai.Client
}

func newOpenaiApi(model string, key string) *openaiApi {
	httpClient := http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}

	openAIConfig := openai.DefaultConfig(key)
	openAIConfig.HTTPClient = &httpClient
	client := openai.NewClientWithConfig(openAIConfig)

	return &openaiApi{model: model, client: client}
}

type chatResult struct {
	responseContent string
	evaluationId    string
}

func (api openaiApi) chat(context context.Context, theirAnswer string, prompt string, wantsJson bool, output *chatResult) (err error) {
	context, span := tracer.Start(context, "chat with AI")
	defer span.End()
	span.SetAttributes(attribute.String("app.llm.model", api.model),
		attribute.String("app.llm.input", theirAnswer),
		attribute.String("app.llm.prompt", prompt),
		attribute.Bool("app.llm.wantsJson", wantsJson),
	)

	startTime := time.Now()
	model := openai.GPT3Dot5Turbo1106

	var responseType openai.ChatCompletionResponseFormatType // go is trash
	if wantsJson {
		responseType = openai.ChatCompletionResponseFormatTypeJSONObject
	} else {
		responseType = openai.ChatCompletionResponseFormatTypeText
	}
	span.SetAttributes(attribute.String("app.llm.responseType", fmt.Sprintf("%v", responseType)))

	openaiMessage := openai.ChatCompletionMessage{Role: "system", Content: prompt}

	openaiChatCompletionResponse, err := api.client.CreateChatCompletion(
		context,
		openai.ChatCompletionRequest{
			ResponseFormat: &openai.ChatCompletionResponseFormat{
				Type: responseType,
			},
			MaxTokens: 2000,
			Model:     model,
			Messages:  []openai.ChatCompletionMessage{openaiMessage},
		},
	)
	if err != nil {
		span.RecordError(err,
			trace.WithAttributes(
				attribute.String("error.message", "Failure talking to OpenAI")))
		span.SetAttributes(attribute.String("error.message", "Failure talking to OpenAI"))
		span.SetStatus(codes.Error, err.Error())

		return err
	}

	addLlmResponseAttributesToSpan(span, openaiChatCompletionResponse)
	llmResponse := openaiChatCompletionResponse.Choices[0].Message.Content

	/* report for analysis */

	interactionReported := deepchecks.DeepChecksAPI{ApiKey: settings.DeepchecksApiKey}.ReportInteraction(context, deepchecks.LLMInteractionDescription{
		FullPrompt: prompt,
		Input:      theirAnswer,
		Output:     llmResponse,
		StartedAt:  startTime,
		FinishedAt: time.Now(),
		Model:      model,
	})

	output.responseContent = llmResponse
	output.evaluationId = interactionReported.EvaluationId

	return
}

type CategoryResult struct {
	Category   string `json:"category"`
	Confidence string `json:"confidence"`
	Reasoning  string `json:"reasoning"`
}

func replaceInString(str string, replacements map[string]string) string {
	for k, v := range replacements {
		str = strings.Replace(str, k, v, -1)
	}
	return str
}

func respondToAnswerV2(currentContext context.Context, questionDefinition Question, answer AnswerBody) (response *responseToAnswer, errorResponse *errorResponseType) {
	span := trace.SpanFromContext(currentContext)

	var question string = questionDefinition.Question
	span.SetAttributes(attribute.String("app.post_answer.question", question))
	llmApi := newOpenaiApi("GPT3Dot5Turbo1106", settings.OpenAIKey)

	/* now use that definition to construct a CATEGORY prompt */
	categoryResult := CategoryResult{}
	{
		categoryPrompt := replaceInString(questionDefinition.PromptsV2.CategoryPrompt, map[string]string{"THEIR_ANSWER": answer.Answer, "QUESTION": questionDefinition.Question})
		span.SetAttributes(attribute.String("app.llm.input", answer.Answer),
			attribute.String("app.llm.category_prompt", categoryPrompt))

		categoryResponse := chatResult{}
		err := llmApi.chat(currentContext, answer.Answer, categoryPrompt, true, &categoryResponse)
		if err != nil {
			return nil, &errorResponseType{message: "Could not reach LLM. No fallback in place", statusCode: 500}

		}
		span.SetAttributes(attribute.String("app.llm.category_response", categoryResponse.responseContent))

		err = json.Unmarshal([]byte(categoryResponse.responseContent), &categoryResult)
		if err != nil {
			return nil, &errorResponseType{message: "Could not parse category response", statusCode: 500}
		}
		span.SetAttributes(attribute.String("app.llm.assigned_category", categoryResult.Category))
	}

	/* now the RESPONSE */
	responseResponse := chatResult{}
	{
		responsePrompt := replaceInString(questionDefinition.PromptsV2.ResponsePrompt, map[string]string{
			"THEIR_ANSWER": answer.Answer,
			"QUESTION":     questionDefinition.Question,
			"CATEGORY":     categoryResult.Category})
		span.SetAttributes(attribute.String("app.llm.input", answer.Answer),
			attribute.String("app.llm.response_prompt", responsePrompt))

		err := llmApi.chat(currentContext, answer.Answer, responsePrompt, false, &responseResponse)
		if err != nil {
			return nil, &errorResponseType{message: "Could not reach LLM. No fallback in place", statusCode: 500}

		}
		span.SetAttributes(attribute.String("app.llm.response", responseResponse.responseContent))
	}

	return &responseToAnswer{response: responseResponse.responseContent, score: 10, evaluationId: responseResponse.evaluationId}, nil
}
