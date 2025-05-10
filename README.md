# graceful-shutdown

This is an example application that shows how to set up a Go-based HTTP server that will cleanly terminate when running
withing Kubernetes.

The example has some `TODO` comments in place for things to be done by someone using this code:
* Setting up [structured logging](https://go.dev/blog/slog).
* Cleaning up external resources, such as database connections. If clean up proves to be far more complicated (e.g.
  ordering issues when cleaning up resources), then consider using
  [Google Wire](https://github.com/google/wire/blob/main/docs/guide.md#cleanup-functions).

## Kubernetes behaviour

When a pod has to be terminated, Kubernetes will mark the pod as `Terminating` (triggering any relevant hooks) as well
as removing the `Pod` from any `Service`. Read the
[Kubernetes documentation](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/) for more information.

Make sure `terminationGracePeriodSeconds` is _more_ than the value of `shutdownPeriod` and `shutdownHardPeriod`
combined.
