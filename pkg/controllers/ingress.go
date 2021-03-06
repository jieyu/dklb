package controllers

import (
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	extsv1beta1 "k8s.io/api/extensions/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	corev1informers "k8s.io/client-go/informers/core/v1"
	extsv1beta1informers "k8s.io/client-go/informers/extensions/v1beta1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	dklbcache "github.com/mesosphere/dklb/pkg/cache"
	"github.com/mesosphere/dklb/pkg/constants"
	"github.com/mesosphere/dklb/pkg/edgelb/manager"
	"github.com/mesosphere/dklb/pkg/metrics"
	"github.com/mesosphere/dklb/pkg/translator"
	kubernetesutil "github.com/mesosphere/dklb/pkg/util/kubernetes"
	"github.com/mesosphere/dklb/pkg/util/prettyprint"
)

const (
	// ingressControllerName is the name of the ingress controller.
	ingressControllerName = "ingress-controller"
	// ingressControllerThreadiness is the number of workers the ingress controller will use to process items from its work queue.
	ingressControllerThreadiness = 1
)

// IngressController is the controller for Ingress resources.
type IngressController struct {
	// IngressController is based-off of a generic controller.
	*genericController
	// kubeClient is a client to the Kubernetes core APIs.
	kubeClient kubernetes.Interface
	// dklbCache is the instance of the Kubernetes resource cache to use.
	kubeCache dklbcache.KubernetesResourceCache
	// edgelbManager is the instance of the EdgeLB manager to use for materializing EdgeLB pools for Ingress resources.
	edgelbManager manager.EdgeLBManager
}

// NewIngressController creates a new instance of the EdgeLB ingress controller.
func NewIngressController(clusterName string, kubeClient kubernetes.Interface, ingressInformer extsv1beta1informers.IngressInformer, serviceInformer corev1informers.ServiceInformer, kubeCache dklbcache.KubernetesResourceCache, edgelbManager manager.EdgeLBManager) *IngressController {
	// Create a new instance of the ingress controller with the specified name and threadiness.
	c := &IngressController{
		genericController: newGenericController(clusterName, ingressControllerName, ingressControllerThreadiness),
		kubeClient:        kubeClient,
		kubeCache:         kubeCache,
		edgelbManager:     edgelbManager,
	}
	// Make the controller wait for the caches to sync.
	c.hasSyncedFuncs = []cache.InformerSynced{
		ingressInformer.Informer().HasSynced,
		kubeCache.HasSynced,
	}
	// Make processQueueItem the handler for items popped out of the work queue.
	c.syncHandler = c.processQueueItem

	// Setup an event handler to inform us when Ingress resources change.
	// An Ingress resource is enqueued in the following scenarios:
	// * It was listed ("ADDED") and has the required "kubernetes.io/ingress.class" annotation set to "edgelb".
	// * It was updated ("MODIFIED") and either the old or the new types - or both - have this same annotation.
	//   * This allows for handling the cases in which the annotation is changed/removed.
	// * It was deleted ("DELETED") and has this same annotation.
	ingressInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			ingress := obj.(*extsv1beta1.Ingress)
			if !kubernetesutil.IsEdgeLBIngress(ingress) {
				return
			}
			c.enqueue(ingress)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldIngress := oldObj.(*extsv1beta1.Ingress)
			newIngress := newObj.(*extsv1beta1.Ingress)
			if !kubernetesutil.IsEdgeLBIngress(oldIngress) && !kubernetesutil.IsEdgeLBIngress(newIngress) {
				return
			}
			c.enqueue(newIngress)
		},
		DeleteFunc: func(obj interface{}) {
			ingress := obj.(*extsv1beta1.Ingress)
			if !kubernetesutil.IsEdgeLBIngress(ingress) {
				return
			}
			c.enqueueTombstone(ingress)
		},
	})
	// Setup an event handler to inform us when Service resources change.
	// This allows us to enqueue all ingresses that reference said Service resource.
	serviceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueReferencingIngresses(obj.(*corev1.Service))
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueReferencingIngresses(obj.(*corev1.Service))
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueReferencingIngresses(obj.(*corev1.Service))
		},
	})

	// Return the instance created above.
	return c
}

// processQueueItem attempts to reconcile the state of the Ingress resource pointed at by the specified key.
func (c *IngressController) processQueueItem(workItem WorkItem) error {
	// Record the current iteration.
	startTime := time.Now()
	metrics.RecordSync(ingressControllerName, workItem.Key)
	defer metrics.RecordSyncDuration(ingressControllerName, startTime)

	// Convert the specified key into a distinct namespace and name.
	namespace, name, err := cache.SplitMetaNamespaceKey(workItem.Key)
	if err != nil {
		runtime.HandleError(fmt.Errorf("invalid resource key %q", workItem.Key))
		return nil
	}

	// Get the Ingress resource with the specified namespace and name.
	ingress, err := c.kubeCache.GetIngress(namespace, name)
	if err == nil {
		// Create a deep copy of the Ingress resource in order to avoid possibly mutating the cache.
		ingress = ingress.DeepCopy()
	} else {
		// Return immediately if the current error's type is something other than "NotFound".
		if !apierrors.IsNotFound(err) {
			return err
		}
		// At this point we know the Ingress resource does not exist anymore.
		// Hence, we take its tombstone and hand it over to the translator so it can perform cleanup of the associated EdgeLB pool as it sees fit.
		if workItem.Tombstone == nil {
			return fmt.Errorf("ingress %q in work queue no longer exists, and no tombstone was recovered", workItem.Key)
		}
		// Create a deep copy of the tombstone in order to avoid mutating the cache.
		ingress = workItem.Tombstone.(*extsv1beta1.Ingress).DeepCopy()
		// Set the current timestamp as the value of ".metadata.deletionTimestamp" so the translator can understand that the resource has been deleted.
		deletionTimestamp := metav1.NewTime(startTime)
		ingress.ObjectMeta.DeletionTimestamp = &deletionTimestamp
	}

	// Create an event recorder that we can use to report events related with the Ingress resource.
	er := kubernetesutil.NewEventRecorderForNamespace(c.kubeClient, ingress.Namespace)

	// Compute the set of options that will be used to translate the Ingress resource into an EdgeLB pool.
	options, err := translator.ComputeIngressTranslationOptions(c.clusterName, ingress)
	if err != nil {
		// Emit an event and log an error, but do not re-enqueue as the resource's spec was found to be invalid.
		er.Eventf(ingress, corev1.EventTypeWarning, constants.ReasonInvalidAnnotations, "the resource's annotations are not valid: %v", err)
		c.logger.Errorf("failed to compute translation options for ingress %q: %v", workItem.Key, err)
		return nil
	}

	// Output some tracing information about the computed set of options.
	prettyprint.LogfSpew(log.Tracef, options, "computed ingress translation options for %q", workItem.Key)

	// Perform translation of the Ingress resource into an EdgeLB pool.
	status, err := translator.NewIngressTranslator(c.clusterName, ingress, *options, c.kubeCache, c.edgelbManager, er).Translate()
	if err != nil {
		er.Eventf(ingress, corev1.EventTypeWarning, constants.ReasonTranslationError, "failed to translate ingress: %v", err)
		c.logger.Errorf("failed to translate ingress %q: %v", workItem.Key, err)
		return err
	}

	// Update the status of the Ingress resource if it hasn't been deleted.
	if ingress.ObjectMeta.DeletionTimestamp == nil && status != nil {
		ingress.Status = extsv1beta1.IngressStatus{LoadBalancer: *status}
		if _, err := c.kubeClient.ExtensionsV1beta1().Ingresses(ingress.Namespace).UpdateStatus(ingress); err != nil {
			c.logger.Errorf("failed to update status for ingress %q: %v", workItem.Key, err)
			return err
		}
	}
	return nil
}

// enqueueReferencingIngresses enqueues Ingress resources that reference the provided Service resource.
func (c *IngressController) enqueueReferencingIngresses(service *corev1.Service) {
	// Grab a list of all Ingress resources in the same namespace as the Service resource.
	ingresses, err := c.kubeCache.GetIngresses(service.Namespace)
	if err != nil {
		c.logger.Errorf("failed to list all ingresses in namespace %q: %v", service.Name, err)
		return
	}
	// Iterate over all Ingress resources in the same namespace, checking whether each one references this Service resource and enqueueing it if it does.
	for _, ingress := range ingresses {
		obj := ingress
		kubernetesutil.ForEachIngresBackend(obj, func(_, _ *string, backend extsv1beta1.IngressBackend) {
			if backend.ServiceName == service.Name {
				c.enqueue(obj)
			}
		})
	}
}
