package main

import (
	"booth_game_lambda/pkg/instrumentation"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/sashabaranov/go-openai"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var postAnswerEndpoint = apiEndpoint{
	"POST",
	"/api/questions/{questionId}/answer",
	regexp.MustCompile("^/api/questions/([^/]+)/answer$"),
	postAnswer,
	true,
}

// const (
// 	start_system_prompt = "You are a quizmaster, who is also an Observability evangelist, validating people's answers who gives a score between 0 and 100. You provide the output as a json object in the format { \"score\": \"{score}\", \"better_answer\": \"{an answer that would improve the score}\"}"
// )

type AnswerBody struct {
	Answer string `json:"answer"`
}

func constructPrompt(prompt AnswerResponsePrompt, question string, answer string) ([]openai.ChatCompletionMessage, string) {
	messages := []openai.ChatCompletionMessage{}

	// Assuming system, examples, question, and next_answer are defined
	var fullPrompt = ""

	messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: prompt.SystemPrompt})
	fullPrompt += "System: " + prompt.SystemPrompt + "\n"

	for _, example := range prompt.Examples {
		messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: question})
		fullPrompt += "Assistant: " + question + "\n"
		messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: example.ExampleAnswer})
		fullPrompt += "User: " + example.ExampleAnswer + "\n"
		messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: example.ExampleResponse})
		fullPrompt += "Assistant: " + example.ExampleResponse + "\n"
	}

	messages = append(messages, openai.ChatCompletionMessage{Role: "assistant", Content: question})
	fullPrompt += "Assistant: " + question + "\n"
	messages = append(messages, openai.ChatCompletionMessage{Role: "user", Content: answer})
	fullPrompt += "User: " + answer + "\n"
	return messages, fullPrompt
}

func postAnswer(currentContext context.Context, request events.APIGatewayV2HTTPRequest) (response events.APIGatewayV2HTTPResponse, err error) {

	currentContext, postQuestionSpan := tracer.Start(currentContext, "Answer Question")
	defer postQuestionSpan.End()
	defer func() {
		// I haven't seen this do anything. I do see the one in main.go doing something
		if r := recover(); r != nil {
			postQuestionSpan.RecordError(r.(error))
			postQuestionSpan.SetStatus(codes.Error, "Panic caught")
			postQuestionSpan.SetAttributes(attribute.String("error.print", fmt.Sprintf("%v", r.(error).Error())))
			response = events.APIGatewayV2HTTPResponse{Body: fmt.Sprintf("Panic caught: %v", r), StatusCode: 500}
		}
	}()

	/* Parse what they sent */
	postQuestionSpan.SetAttributes(attribute.String("request.body", request.Body))
	answer := AnswerBody{}
	err = json.Unmarshal([]byte(request.Body), &answer)
	if err != nil {
		newErr := fmt.Errorf("error unmarshalling answer: %w\n request body: %s", err, request.Body)
		postQuestionSpan.RecordError(newErr)
		return events.APIGatewayV2HTTPResponse{Body: "Bad request. Expected format: { 'answer': 'stuff' }", StatusCode: 400}, nil
	}

	/* what question are they referring to? */
	eventName := getEventName(request)
	postQuestionSpan.SetAttributes(attribute.String("app.post_answer.event_name", eventName))
	path := request.RequestContext.HTTP.Path
	pathSplit := strings.Split(path, "/")
	questionId := pathSplit[3]
	postQuestionSpan.SetAttributes(attribute.String("app.post_answer.question_id", questionId))

	/* find that question in our question definitions */
	var question string
	var openaiMessages []openai.ChatCompletionMessage
	var promptSpec AnswerResponsePrompt
	var fullPrompt string
	eventQuestions := eventQuestions[eventName]

	for _, v := range eventQuestions {
		if v.Id.String() == questionId {
			promptSpec = v.AnswerResponsePrompt
			question = v.Question
			break
		}
	}
	if question == "" {
		postQuestionSpan.SetAttributes(attribute.String("error.message", "Couldn't find question"))
		postQuestionSpan.SetStatus(codes.Error, "Couldn't find question")
		return events.APIGatewayV2HTTPResponse{Body: "Couldn't find question with that ID", StatusCode: 404}, nil
	}
	postQuestionSpan.SetAttributes(attribute.String("app.post_answer.question", question))

	/* now use that definition to construct a prompt */
	openaiMessages, fullPrompt = constructPrompt(promptSpec, question, answer.Answer)
	postQuestionSpan.SetAttributes(attribute.String("app.llm.input", answer.Answer),
		attribute.String("app.llm.full_prompt", fullPrompt))

	/* now call OpenAI */
	httpClient := http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}

	openAIConfig := openai.DefaultConfig(settings.OpenAIKey)
	openAIConfig.HTTPClient = &httpClient
	client := openai.NewClientWithConfig(openAIConfig)

	startTime := time.Now()
	resp, err := client.CreateChatCompletion(
		currentContext,
		openai.ChatCompletionRequest{
			// ResponseFormat: &openai.ChatCompletionResponseFormat{
			// 	Type: openai.ChatCompletionResponseFormatTypeJSONObject,
			// },
			MaxTokens: 2000,
			Model:     openai.GPT3Dot5Turbo1106,
			Messages:  openaiMessages,
		},
	)
	if err != nil {
		postQuestionSpan.RecordError(err,
			trace.WithAttributes(
				attribute.String("error.message", "Failure talking to OpenAI")))
		postQuestionSpan.SetAttributes(attribute.String("error.message", "Failure talking to OpenAI"))
		postQuestionSpan.SetStatus(codes.Error, err.Error())

		response := events.APIGatewayV2HTTPResponse{Body: `{ "message": "Could not reach LLM. No fallback in place" }`, StatusCode: 500}

		instrumentation.InjectTraceParentToResponse(postQuestionSpan, &response)

		return response, nil
	}
	addLlmResponseAttributesToSpan(postQuestionSpan, resp)
	llmResponse := resp.Choices[0].Message.Content

	/* report for analysis */
	tellDeepChecksAboutIt(currentContext, LLMInteractionDescription{
		FullPrompt: fullPrompt,
		Input:      answer.Answer,
		Output:     llmResponse,
		StartedAt:  startTime,
		FinishedAt: time.Now(),
	})

	/* tell the UI what we got */
	postQuestionSpan.SetAttributes(attribute.String("app.llm.output", llmResponse))
	result := PostAnswerResponse{Response: llmResponse, Score: 100}
	jsonData, err := json.Marshal(result)
	if err != nil {
		postQuestionSpan.RecordError(err, trace.WithAttributes(attribute.String("error.message", "Failure marshalling JSON")))
		return events.APIGatewayV2HTTPResponse{Body: "wtaf", StatusCode: 500}, nil
	}

	return events.APIGatewayV2HTTPResponse{Body: string(jsonData), StatusCode: 200}, nil
}

type PostAnswerResponse struct {
	Response string `json:"response"`
	Score    int    `json:"score"`
}

func addLlmResponseAttributesToSpan(span trace.Span, llmResponse openai.ChatCompletionResponse) {
	span.SetAttributes(attribute.String("app.llm.response", llmResponse.Choices[0].Message.Content),
		attribute.String("app.llm.response_id", llmResponse.ID),
		attribute.Int("app.llm.prompt_tokens", llmResponse.Usage.PromptTokens),
		attribute.Int("app.llm.completion_tokens", llmResponse.Usage.CompletionTokens),
		attribute.Int("app.llm.total_tokens", llmResponse.Usage.TotalTokens),
	)
}