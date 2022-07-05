package server

import (
	any "github.com/golang/protobuf/ptypes/any"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	istioapiv1alpha3 "istio.io/api/networking/v1alpha3"
	istiov1alpha3 "istio.io/client-go/pkg/apis/networking/v1alpha3"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func getTypeUrlFromObject(obj client.Object) string {
	gvk := obj.GetObjectKind().GroupVersionKind()
	return gvk.Group + "/" + gvk.Version + "/" + gvk.Kind
}

func convertToMCPResource(list client.ObjectList, logger *logrus.Entry) []*any.Any {

	resources := make([]*any.Any, 0, 0)

	switch l := list.(type) {
	case *istiov1alpha3.VirtualServiceList:
		for _, vs := range l.Items {
			v := &istioapiv1alpha3.VirtualService{
				Hosts:    vs.Spec.Hosts,
				Gateways: vs.Spec.Gateways,
				Http:     vs.Spec.Http,
				Tls:      vs.Spec.Tls,
				ExportTo: vs.Spec.ExportTo,
				Tcp:      vs.Spec.Tcp,
			}

			var vsAny anypb.Any

			if err := anypb.MarshalFrom(&vsAny, v, proto.MarshalOptions{}); err != nil {
				logger.WithError(err).WithField("name", vs.Name).Error("Failed to marshal virtual service")
				continue
			}

		}

	case *istiov1alpha3.DestinationRuleList:

	}

	return resources

}
