package registry

import (
	"context"
	"fmt"

	"istio.io/api/label"
	istioapiv1alpha3 "istio.io/api/networking/v1alpha3"
	"istio.io/istio/pilot/pkg/model"
	istio_controller "istio.io/istio/pilot/pkg/serviceregistry/kube/controller"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

//convertEndpointsToServiceEntry creates a serialized istioapiv1alpha3.Service Entry from an corev1.Endpoints
func (r *registry) convertEndpointsToServiceEntry(ep *corev1.Endpoints, clusterDomain string) (*istioapiv1alpha3.ServiceEntry, error) {

	entries := make([]*istioapiv1alpha3.WorkloadEntry, 0, 0)

	svc, err := r.clientSet.CoreV1().Services(ep.Namespace).Get(context.Background(), ep.Name, metav1.GetOptions{})

	if err != nil {
		r.logger.
			WithError(err).
			WithField("endpoints", ep.Namespace+"/"+ep.Name).
			Error("Failed to get the service for the endpoints")
	}

	ports := make([]*istioapiv1alpha3.Port, 0, len(svc.Spec.Ports))

	for _, port := range svc.Spec.Ports {

		port := &istioapiv1alpha3.Port{
			Number:   uint32(port.Port),
			Protocol: "HTTP",
			Name:     port.Name,
			// TODO: support named ports
			TargetPort: uint32(port.TargetPort.IntValue()),
		}

		ports = append(ports, port)
	}

	for _, subset := range ep.Subsets {

		for _, address := range subset.Addresses {

			entry := &istioapiv1alpha3.WorkloadEntry{
				Address: address.IP,
			}

			if target := address.TargetRef; target != nil {
				switch target.Kind {
				case "Pod":
					// TODO: Improve the reliability of this query
					pod, err := r.clientSet.CoreV1().Pods(target.Namespace).Get(context.Background(), target.Name, metav1.GetOptions{})
					if err != nil && !k8s_errors.IsNotFound(err) {
						// Ignore the pod if there is
						r.logger.WithError(err).WithField("endpoint", ep.Namespace+"/"+ep.Name).WithField("pod", target.Namespace+"/"+target.Name).Warn("Failed to get pod")
						continue
					} else if k8s_errors.IsNotFound(err) {
						r.logger.WithField("endpoint", ep.Namespace+"/"+ep.Name).WithField("pod", target.Namespace+"/"+target.Name).Warn("Pod not found")
						continue
					}

					entry.Labels = pod.Labels
					entry.Locality = r.getPodLocality(pod)

				default:
					return nil, fmt.Errorf("convertEndpointsToServiceEntry not implemented for %s", target.Kind)
				}

			}

			entries = append(entries, entry)

		}

	}

	serviceEntry := &istioapiv1alpha3.ServiceEntry{
		Hosts:      []string{getServiceNameFromMetadata(&svc.ObjectMeta, clusterDomain)},
		Resolution: istioapiv1alpha3.ServiceEntry_STATIC,
		ExportTo:   []string{"*"},
		Endpoints:  entries,
		Ports:      ports,
	}

	return serviceEntry, nil

}

func getServiceNameFromMetadata(metadata *metav1.ObjectMeta, clusterDomain string) string {
	return metadata.Name + "." + metadata.Namespace + ".svc." + clusterDomain
}

// Strongly based on Istio Function itself
// getPodLocality retrieves the locality for a pod.
func (r *registry) getPodLocality(pod *v1.Pod) string {
	// if pod has `istio-locality` label, skip below ops
	if len(pod.Labels[model.LocalityLabel]) > 0 {
		return model.GetLocalityLabelOrDefault(pod.Labels[model.LocalityLabel], "")
	}

	// NodeName is set by the scheduler after the pod is created
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#late-initialization
	raw, err := r.clientSet.CoreV1().Nodes().Get(context.Background(), pod.Spec.NodeName, metav1.GetOptions{})
	if err != nil {
		if pod.Spec.NodeName != "" {
			r.logger.
				WithError(err).
				WithField("pod", pod.Name).
				WithField("namespace", pod.Name).
				WithField("node", pod.Spec.NodeName).
				Warn("unable to get node  for pod")
		}
		return ""
	}

	nodeMeta, err := meta.Accessor(raw)
	if err != nil {
		r.logger.WithField("node", pod.Spec.NodeName).WithField("nodeMetadata", nodeMeta).Warn("unable to get node meta")
		return ""
	}

	region := getLabelValue(nodeMeta, istio_controller.NodeRegionLabel, istio_controller.NodeRegionLabelGA)
	zone := getLabelValue(nodeMeta, istio_controller.NodeZoneLabel, istio_controller.NodeZoneLabelGA)
	subzone := getLabelValue(nodeMeta, label.TopologySubzone.Name, "")

	if region == "" && zone == "" && subzone == "" {
		return ""
	}

	return region + "/" + zone + "/" + subzone // Format: "%s/%s/%s"
}

func getLabelValue(metadata metav1.Object, label string, fallBackLabel string) string {
	metaLabels := metadata.GetLabels()
	val := metaLabels[label]
	if val != "" {
		return val
	}

	return metaLabels[fallBackLabel]
}
