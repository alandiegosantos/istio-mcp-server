package registry

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alandiegosantos/istio-mcp/pkg/util"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	mcp "istio.io/api/mcp/v1alpha1"
	istiov1alpha3 "istio.io/client-go/pkg/apis/networking/v1alpha3"
	versionedclient "istio.io/client-go/pkg/clientset/versioned"
	istio_internalinterfaces "istio.io/client-go/pkg/informers/externalversions/internalinterfaces"
	clientgo_istio_networking_v1alpha3 "istio.io/client-go/pkg/informers/externalversions/networking/v1alpha3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	clientgo_core_v1 "k8s.io/client-go/informers/core/v1"
	internalinterfaces "k8s.io/client-go/informers/internalinterfaces"
)

const (

	// debounceMax is the maximum time to wait for events while debouncing.
	// Defaults to 10 seconds. If events keep showing up with no break for this time, we'll trigger a push.
	debounceMax = 10 * time.Second

	// debounceAfter is the delay added to events to wait after a registry event for debouncing.
	// This will delay the push by at least this interval, plus the time getting subsequent events.
	// If no change is detected the push will happen, otherwise we'll keep delaying until things settle.
	debounceAfter = 500 * time.Millisecond

	// List of typeURLs in the xDS request from istiod
	VirtualServiceTypeUrl  = "networking.istio.io/v1alpha3/VirtualService"
	DestinationRuleTypeUrl = "networking.istio.io/v1alpha3/DestinationRule"
	ServiceEntryTypeUrl    = "networking.istio.io/v1alpha3/ServiceEntry"
)

// Informers on Istio client-go
var istioInformerPerTypeUrl = map[string]func(versionedclient.Interface, string, time.Duration, cache.Indexers, istio_internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer{
	VirtualServiceTypeUrl:  clientgo_istio_networking_v1alpha3.NewFilteredVirtualServiceInformer,
	DestinationRuleTypeUrl: clientgo_istio_networking_v1alpha3.NewFilteredDestinationRuleInformer,
}

// Informers on standard client-go
var informerPerTypeUrl = map[string]func(client kubernetes.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer{
	ServiceEntryTypeUrl: clientgo_core_v1.NewFilteredEndpointsInformer, // Service Entries are generated based on corev1.Endpoints
}

var (
	resyncInterval       = 1 * time.Minute
	callbackCnt    int64 = 0
)

type Registry interface {
	RegisterCallback(c Callback) func()
}

type registry struct {
	ctx            context.Context
	cancelCtx      context.CancelFunc
	logger         *logrus.Entry
	istioClientset *versionedclient.Clientset
	clientSet      *kubernetes.Clientset
	informers      map[string]cache.SharedIndexInformer
	stopCh         chan struct{}

	selector labels.Selector

	callbacks      map[int64]Callback
	callbacksMutex sync.RWMutex

	clusterDomain string
}

type Callback func(typeUrl string, resources []*anypb.Any)

// NewRegistry returns a new registry that watches for the cluster and create informers
// for multiple resources
func NewRegistry(config *rest.Config, resourcesSelector labels.Selector, clusterDomain string) (*registry, error) {

	istioClient, err := versionedclient.NewForConfig(config)
	if err != nil {
		return nil, errors.Wrap(err, "failed to start istio clientset")
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, errors.Wrap(err, "failed to start clientset")
	}

	stopCh := make(chan struct{})

	r := &registry{
		clientSet:      client,
		istioClientset: istioClient,
		logger:         util.LoggerFor("registry"),
		stopCh:         stopCh,
		informers:      make(map[string]cache.SharedIndexInformer),
		callbacks:      make(map[int64]Callback),
		selector:       resourcesSelector,
		clusterDomain:  clusterDomain,
	}

	return r, nil

}

func (r *registry) RegisterCallback(c Callback) func() {
	r.callbacksMutex.Lock()
	defer r.callbacksMutex.Unlock()
	callbackId := atomic.AddInt64(&callbackCnt, 1)
	r.callbacks[callbackId] = c
	return r.deregisterCallback(callbackId)
}

func (r *registry) deregisterCallback(callbackId int64) func() {
	return func() {
		r.callbacksMutex.Lock()
		defer r.callbacksMutex.Unlock()

		delete(r.callbacks, callbackId)
	}
}

func (r *registry) Start(ctx context.Context) error {

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		<-ctx.Done()
		close(r.stopCh)
		return nil
	})

	tweakListOptionsFunc := func(options *metav1.ListOptions) {
		options.LabelSelector = r.selector.String()
	}

	// Start informers using Istio client-go
	for typeUrl, factory := range istioInformerPerTypeUrl {
		informer := factory(
			r.istioClientset,
			metav1.NamespaceAll,
			resyncInterval,
			cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
			tweakListOptionsFunc,
		)
		r.informers[typeUrl] = informer
		runInformer, updateCacheWithDebounce := r.startInformer(typeUrl)
		g.Go(runInformer)
		g.Go(updateCacheWithDebounce)
	}

	// Start informers using default client-go
	for typeUrl, factory := range informerPerTypeUrl {
		informer := factory(
			r.clientSet,
			metav1.NamespaceAll,
			resyncInterval,
			cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
			tweakListOptionsFunc,
		)
		r.informers[typeUrl] = informer
		runInformer, updateCacheWithDebounce := r.startInformer(typeUrl)
		g.Go(runInformer)
		g.Go(updateCacheWithDebounce)
	}

	return g.Wait()
}

// startInformer is responsible for creating the structs and the functions
// that watch the resources and update the cache, so we can add a debounce approach
// per typeURL
func (r *registry) startInformer(typeUrl string) (func() error, func() error) {

	informer, found := r.informers[typeUrl]
	if !found {
		panic("The informer is not registered in the registry")
	}

	pushEvents := make(chan struct{}, 10)

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pushEvents <- struct{}{}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			pushEvents <- struct{}{}
		},
		DeleteFunc: func(obj interface{}) {
			pushEvents <- struct{}{}
		},
	})

	runInformer := func() error {
		r.logger.WithField("typeUrl", typeUrl).Info("Starting informer")
		informer.Run(r.stopCh)
		return nil
	}

	updateCache := func() error {
		var timeChan <-chan time.Time
		var startDebounce time.Time
		var lastResourceUpdateTime time.Time
		debouncedEvents := 0

		if !cache.WaitForCacheSync(r.stopCh, informer.HasSynced) {
			return fmt.Errorf("informer for %v has not synced", typeUrl)
		}

		for {
			select {
			case <-pushEvents:
				r.logger.WithField("typeUrl", typeUrl).Info("received event from push channel")

				lastResourceUpdateTime = time.Now()
				if debouncedEvents == 0 {
					r.logger.WithField("typeUrl", typeUrl).Debug("This is the first debounced event")
					startDebounce = time.Now()
				}
				timeChan = time.After(debounceAfter)
				debouncedEventsCnt.WithLabelValues(typeUrl).Inc()
				debouncedEvents++
			case <-timeChan:
				r.logger.WithField("typeUrl", typeUrl).Info("received event from time channel")
				eventDelay := time.Since(startDebounce)
				quietTime := time.Since(lastResourceUpdateTime)
				if eventDelay >= debounceMax || quietTime >= debounceAfter {
					r.callCallbacks(typeUrl)
					debouncedEvents = 0
				}
			case <-r.stopCh:
				return nil
			}
		}
	}

	return runInformer, updateCache

}

func (r *registry) callCallbacks(typeUrl string) {

	if informer, found := r.informers[typeUrl]; found {

		objs := informer.GetStore().List()
		resources := make([]*anypb.Any, 0, len(objs))

		for _, obj := range objs {

			if res, err := r.convertToMcpResource(obj); err == nil {
				resources = append(resources, res)
			} else {
				r.logger.WithError(err).Errorf("failed to convert")
			}
		}

		if len(resources) == 0 {
			return
		}

		r.logger.WithField("typeUrl", typeUrl).WithField("num_callbacks", len(r.callbacks)).Info("triggering callbacks")
		r.callbacksMutex.RLock()
		for _, callback := range r.callbacks {
			callback(typeUrl, resources)
		}
		r.callbacksMutex.RUnlock()

	}

}

func (r *registry) convertToMcpResource(obj interface{}) (*anypb.Any, error) {

	var objAny anypb.Any
	var metadata *metav1.ObjectMeta

	switch v := obj.(type) {
	case *istiov1alpha3.DestinationRule:
		metadata = &v.ObjectMeta
		if err := anypb.MarshalFrom(&objAny, &v.Spec, proto.MarshalOptions{}); err != nil {
			resourceType := fmt.Sprintf("%T", v)
			convertionErrorsCnt.WithLabelValues("resourceType").Inc()
			return nil, errors.Wrap(err, "failed to marshal "+resourceType)
		}

	case *istiov1alpha3.VirtualService:
		metadata = &v.ObjectMeta
		if err := anypb.MarshalFrom(&objAny, &v.Spec, proto.MarshalOptions{}); err != nil {
			resourceType := fmt.Sprintf("%T", v)
			convertionErrorsCnt.WithLabelValues("resourceType").Inc()
			return nil, errors.Wrap(err, "failed to marshal "+resourceType)
		}

	case *corev1.Endpoints:
		metadata = &v.ObjectMeta
		serviceEntry, err := r.convertEndpointsToServiceEntry(v, r.clusterDomain)
		if err != nil {
			convertionErrorsCnt.WithLabelValues("resourceType").Inc()
			return nil, errors.Wrap(err, "failed to convert endpoint")
		}

		if err := anypb.MarshalFrom(&objAny, serviceEntry, proto.MarshalOptions{}); err != nil {
			resourceType := fmt.Sprintf("%T", v)
			convertionErrorsCnt.WithLabelValues("resourceType").Inc()
			return nil, errors.Wrap(err, "failed to marshal "+resourceType)
		}

	default:
		resourceType := fmt.Sprintf("%T", obj)
		r.logger.WithField("type", resourceType).Warn("Ignoring received resource in convertToMcpResource")
		convertionErrorsCnt.WithLabelValues("resourceType").Inc()
		return nil, fmt.Errorf("failed to convert " + resourceType)
	}

	resource := &mcp.Resource{
		Metadata: &mcp.Metadata{
			Name:        metadata.Namespace + "/" + metadata.Name,
			Labels:      metadata.GetLabels(),
			Annotations: metadata.GetAnnotations(),
		},
		Body: &objAny,
	}

	var rsAny anypb.Any
	if err := anypb.MarshalFrom(&rsAny, resource, proto.MarshalOptions{}); err != nil {
		return nil, errors.Wrap(err, "failed to marshal mcp.Resource")
	}

	return &rsAny, nil

}
