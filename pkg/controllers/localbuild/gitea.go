package localbuild

import (
	"context"
	"embed"
	"errors"
	"time"

	"github.com/cnoe-io/idpbuilder/api/v1alpha1"
	"github.com/cnoe-io/idpbuilder/pkg/k8s"
	"github.com/cnoe-io/idpbuilder/pkg/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

//go:embed resources/gitea/k8s/*
var installGiteaFS embed.FS
var timeout = time.After(30 * time.Second)

const (
	giteaServerName string = "my-gitea"
)

const giteaResourceName = "gitea"

func GetRawGiteaInstallResources() ([][]byte, error) {
	return util.ConvertFSToBytes(installGiteaFS, "resources/gitea/k8s")
}

func GetK8sGiteaInstallResources(scheme *runtime.Scheme) ([]client.Object, error) {
	rawResources, err := GetRawGiteaInstallResources()
	if err != nil {
		return nil, err
	}

	return k8s.ConvertRawResourcesToObjects(scheme, rawResources)
}

func newGiteaNamespace() *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gitea",
		},
	}
}

func (r *LocalbuildReconciler) ReconcileGitea(ctx context.Context, req ctrl.Request, resource *v1alpha1.Localbuild) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	if resource.Spec.PackageConfigs.GitConfig.Type != giteaResourceName {
		log.Info("Gitea installation disabled, skipping")
		return ctrl.Result{}, nil
	}

	// Install Gitea
	giteansClient := client.NewNamespacedClient(r.Client, "gitea")
	installObjs, err := GetK8sGiteaInstallResources(r.Scheme)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Ensure namespace exists
	giteaNS := newGiteaNamespace()
	if err = r.Client.Get(ctx, types.NamespacedName{Name: "gitea"}, giteaNS); err != nil {
		// We got an error so try creating the NS
		if err = r.Client.Create(ctx, giteaNS); err != nil {
			return ctrl.Result{}, err
		}
	}

	log.Info("Installing gitea resources")
	for _, obj := range installObjs {
		if obj.GetObjectKind().GroupVersionKind().Kind == "Deployment" {
			switch obj.GetName() {
			case giteaServerName:
				gotObj := appsv1.Deployment{}
				if err := r.Client.Get(ctx, types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}, &gotObj); err != nil {
					if err = controllerutil.SetControllerReference(resource, obj, r.Scheme); err != nil {
						log.Error(err, "Setting controller reference for Gitea deployment", "deployment", obj)
						return ctrl.Result{}, err
					}
				}
			}
		}

		// Create object
		if err = k8s.EnsureObject(ctx, giteansClient, obj, "gitea"); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Wait for Gitea to become available
	ready := make(chan bool)
	go func() {
		for {
			for _, obj := range installObjs {
				if obj.GetObjectKind().GroupVersionKind().Kind == "Deployment" {
					switch obj.GetName() {
					case giteaServerName:
						gotObj := appsv1.Deployment{}
						if gotObj.Status.AvailableReplicas >= 1 {
							ready <- true
							return
						}
					}
				}
			}

			time.Sleep(1 * time.Second)
		}
	}()

	select {
	case <-timeout:
		err := errors.New("Timeout")
		log.Error(err, "Didn't reconcile Gitea on time.")
		return ctrl.Result{}, err
	case <-ready:
		log.Info("Gitea is ready!")
		resource.Status.GiteaAvailable = true
	}

	return ctrl.Result{}, nil
}
