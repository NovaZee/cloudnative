package main

import (
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/component-base/logs"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	"os"
	"time"

	overcommitplugin "cloudnative/pkg/media-scheduler/plugins/overcommit"
	roomaffinityplugin "cloudnative/pkg/media-scheduler/plugins/roomaffinity"
)

var mediaSchedulerPlugins = map[string]frameworkruntime.PluginFactory{
	overcommitplugin.Name:   overcommitplugin.New,
	roomaffinityplugin.Name: roomaffinityplugin.New,
}

// pluginsOptions returns the plugin factory functions wrapped for registration.
func pluginsOptions() []app.Option {
	options := make([]app.Option, 0, len(mediaSchedulerPlugins))
	for name, factoryFn := range mediaSchedulerPlugins {
		wrapped := func(name string, factory frameworkruntime.PluginFactory) app.Option {
			return app.WithPlugin(name, factoryFn)
		}
		options = append(options, wrapped(name, factoryFn))
	}
	return options
}

func main() {
	rand.Seed(time.Now().UnixNano())

	command := app.NewSchedulerCommand(
		pluginsOptions()...,
	)

	logs.InitLogs()
	defer logs.FlushLogs()

	if err := command.Execute(); err != nil {
		os.Exit(1)
	}
}
