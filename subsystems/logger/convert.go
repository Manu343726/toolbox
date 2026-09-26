package logger

import (
	"fmt"
	"log/slog"
	"sort"

	loggerv1 "github.com/Manu343726/toolbox/subsystems/logger/loggerv1"

	"github.com/Manu343726/toolbox/pkg/log"
)

// This file is the RPC boundary and the only place a message shape exists. Everything it
// produces is a slog type and everything it consumes is one, so a change to the contract
// cannot reach past this file into the router.

// levelFromProto converts a contract severity to the standard library's.
func levelFromProto(level loggerv1.LogLevel) slog.Level {
	switch level {
	case loggerv1.LogLevel_LOG_LEVEL_DEBUG:
		return slog.LevelDebug
	case loggerv1.LogLevel_LOG_LEVEL_WARN:
		return slog.LevelWarn
	case loggerv1.LogLevel_LOG_LEVEL_ERROR:
		return slog.LevelError
	default:
		// Unspecified is info, which is the standard library's own zero value and therefore
		// what a caller that did not choose a severity means.
		return slog.LevelInfo
	}
}

// levelToProto converts a severity to the contract's.
//
// A level below debug or above error still lands on one of the four, because a deployment
// that configured an unusual level should be reported at the nearest one in the contract
// rather than reported as none: a reader told "info" can still act, and a reader told
// nothing has been told at all.
func levelToProto(level slog.Level) loggerv1.LogLevel {
	switch {
	case level <= slog.LevelDebug:
		return loggerv1.LogLevel_LOG_LEVEL_DEBUG
	case level >= slog.LevelError:
		return loggerv1.LogLevel_LOG_LEVEL_ERROR
	case level >= slog.LevelWarn:
		return loggerv1.LogLevel_LOG_LEVEL_WARN
	default:
		return loggerv1.LogLevel_LOG_LEVEL_INFO
	}
}

// handlersToProto renders the sinks in the order the configuration declared them.
//
// The order is the sorted one ParseConfig produced, so a caller reading the fanout twice sees
// the same order twice rather than whatever a map iteration happened to do.
func handlersToProto(handlers []log.HandlerConfig) []*loggerv1.LogHandler {
	out := make([]*loggerv1.LogHandler, 0, len(handlers))
	for _, handler := range handlers {
		declared := &loggerv1.LogHandler{
			Name:     handler.Name,
			Provider: handler.Provider,
			Level:    levelToProto(handler.LevelOrInfo()),
		}
		if len(handler.Options) > 0 {
			declared.Options = make(map[string]string, len(handler.Options))
			for key, value := range handler.Options {
				declared.Options[key] = renderOption(value)
			}
		}
		out = append(out, declared)
	}
	return out
}

// renderOption renders a setting's value as text, which is all the contract carries.
//
// A setting that is a number or a flag reaches a reader as the text a person would have
// written, because a reader asking "what is this file's max_backups" wants the answer they
// could have typed.
func renderOption(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case slog.Level:
		return typed.String()
	case slog.Leveler:
		return typed.Level().String()
	default:
		return fmt.Sprint(value)
	}
}

// fanoutFromProto reads a fanout a caller supplied for its own workspace.
//
// The messages are the ones the deployment reports its own fanout with, so there is one shape
// for a fanout in both directions and a caller that read GetConfig can send back what it read.
// The rules a deployment's configuration follows apply here unchanged: every matching route
// contributes, a sink named by two routes receives an entry once, a route that tags entries
// and sends them nowhere is refused, and a condition is text.
func fanoutFromProto(level loggerv1.LogLevel, handlers []*loggerv1.LogHandler, routes []*loggerv1.LogRoute) (log.Config, error) {
	fanout := log.Config{Level: levelFromProto(level)}
	if level == loggerv1.LogLevel_LOG_LEVEL_UNSPECIFIED {
		// A caller that said nothing is not choosing a minimum; the deployment's applies.
		// Zero would be debug, which is the one value that widens rather than narrows.
		fanout.Level = slog.LevelInfo
	}
	for index, handler := range handlers {
		if handler.GetName() == "" {
			return log.Config{}, fmt.Errorf("handler %d needs a name: a route naming it would be ambiguous", index+1)
		}
		if handler.GetProvider() == "" {
			return log.Config{}, fmt.Errorf("the %q handler needs a provider: which backend writes to it", handler.GetName())
		}
		declared := log.HandlerConfig{
			Name:     handler.GetName(),
			Provider: handler.GetProvider(),
			Options:  map[string]any{},
		}
		if handler.GetLevel() != loggerv1.LogLevel_LOG_LEVEL_UNSPECIFIED {
			parsed := levelFromProto(handler.GetLevel())
			declared.Level = &parsed
		}
		for key, value := range handler.GetOptions() {
			declared.Options[key] = value
		}
		fanout.Handlers = append(fanout.Handlers, declared)
	}
	sort.Slice(fanout.Handlers, func(i, j int) bool { return fanout.Handlers[i].Name < fanout.Handlers[j].Name })
	for index, route := range routes {
		if len(route.GetHandlers()) == 0 {
			return log.Config{}, fmt.Errorf("route %d must name the handlers it sends entries to", index+1)
		}
		converted := log.Route{Name: route.GetName(), Handlers: route.GetHandlers()}
		if route.GetLevel() != loggerv1.LogLevel_LOG_LEVEL_UNSPECIFIED {
			parsed := levelFromProto(route.GetLevel())
			converted.When.Level = &parsed
		}
		if len(route.GetAttributes()) > 0 {
			converted.When.Attributes = make(map[string]string, len(route.GetAttributes()))
			for key, value := range route.GetAttributes() {
				converted.When.Attributes[key] = value
			}
		}
		if len(route.GetAdd()) > 0 {
			converted.Add = make(map[string]string, len(route.GetAdd()))
			for key, value := range route.GetAdd() {
				converted.Add[key] = value
			}
		}
		fanout.Routes = append(fanout.Routes, converted)
	}
	if len(fanout.Handlers) == 0 {
		return log.Config{}, fmt.Errorf("a fanout needs at least one handler: it describes where entries go")
	}
	return fanout, nil
}

// routesToProto renders the delivery plan in order, because a route's position is what
// decides which one claims a shared sink first.
func routesToProto(routes []log.Route) []*loggerv1.LogRoute {
	out := make([]*loggerv1.LogRoute, 0, len(routes))
	for index, route := range routes {
		declared := &loggerv1.LogRoute{
			Name:     route.Name,
			Handlers: route.Handlers,
		}
		if declared.Name == "" {
			// A route that named itself has nothing to identify it by otherwise, and a
			// diagnostic about "route 2" is one a caller cannot act on.
			declared.Name = fmt.Sprintf("route %d", index+1)
		}
		if route.When.Level != nil {
			declared.Level = levelToProto(*route.When.Level)
		}
		if len(route.When.Attributes) > 0 {
			declared.Attributes = make(map[string]string, len(route.When.Attributes))
			for key, value := range route.When.Attributes {
				declared.Attributes[key] = value
			}
		}
		if len(route.Add) > 0 {
			declared.Add = make(map[string]string, len(route.Add))
			for key, value := range route.Add {
				declared.Add[key] = value
			}
		}
		out = append(out, declared)
	}
	return out
}
