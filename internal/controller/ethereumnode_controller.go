package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"crypto/rand"

	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	infrav1 "github.com/mvleandro/blockchain-operator/api/v1"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var (
	nodeReconcileErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ethereum_node_reconcile_errors_total",
			Help: "Total number of errors while reconciling Ethereum Nodes",
		},
		[]string{"node_name"},
	)

	nodeBlockHeight = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ethereum_node_block_height",
			Help: "The current block height of the ethereum node observed via JSON-RPC",
		},
		[]string{"node_name", "network", "pod_name"},
	)
)

func init() {
	metrics.Registry.MustRegister(nodeReconcileErrors)
	metrics.Registry.MustRegister(nodeBlockHeight)
}

// EthereumNodeReconciler reconciles a EthereumNode object
type EthereumNodeReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// --- RBAC Permissions ---
//+kubebuilder:rbac:groups=infra.blockchain.corp,resources=ethereumnodes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=infra.blockchain.corp,resources=ethereumnodes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

func (r *EthereumNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// 1. EthereumNode object fetch
	var ethNode infrav1.EthereumNode
	if err := r.Get(ctx, req.NamespacedName, &ethNode); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 1.5 Ensure Headless Service
	if err := r.reconcileService(ctx, &ethNode); err != nil {
		return ctrl.Result{}, err
	}

	// 1.6 Ensure P2P Service (LoadBalancer)
	if err := r.reconcileP2PService(ctx, &ethNode); err != nil {
		log.Error(err, "Failed to reconcile P2P Service")
		return ctrl.Result{}, err
	}

	// 1.7 Ensure JWT Secret
	// The operator creates the secret before the Pod is created
	if err := r.reconcileJWTSecret(ctx, &ethNode); err != nil {
		log.Error(err, "Failed to reconcile JWT Secret")
		return ctrl.Result{}, err
	}

	// 2. Define desired StatefulSet state
	desiredSts, err := r.desiredStatefulSet(&ethNode)
	if err != nil {
		log.Error(err, "Failed to define desired StatefulSet")
		// Raise an erro
		r.Recorder.Event(&ethNode, corev1.EventTypeWarning, "SpecError", "Invalid specification for StatefulSet")
		return ctrl.Result{}, err
	}

	// 3. Check for existing StatefulSet
	var existingSts appsv1.StatefulSet
	err = r.Get(ctx, types.NamespacedName{Name: ethNode.Name, Namespace: ethNode.Namespace}, &existingSts)

	if err != nil && apierrors.IsNotFound(err) {
		// --- CREATE ---
		log.Info("Creating new StatefulSet", "StatefulSet.Name", desiredSts.Name)
		if err := r.Create(ctx, desiredSts); err != nil {
			log.Error(err, "Failed to create StatefulSet")
			nodeReconcileErrors.WithLabelValues(ethNode.Name).Inc()
			return ctrl.Result{}, err
		}

		// Raises an event to indicate successful creation
		r.Recorder.Eventf(&ethNode, corev1.EventTypeNormal, "Created", "Created StatefulSet %s", desiredSts.Name)
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}

	// --- UPDATE / DRIFT DETECTION ---
	desiredHash := desiredSts.Annotations["blockchain.corp/spec-hash"]
	existingHash := existingSts.Annotations["blockchain.corp/spec-hash"]

	if desiredHash != existingHash {
		log.Info("Drift detected! Updating StatefulSet...", "OldHash", existingHash, "NewHash", desiredHash)

		existingSts.Spec.Replicas = desiredSts.Spec.Replicas
		existingSts.Spec.Template = desiredSts.Spec.Template

		if existingSts.Annotations == nil {
			existingSts.Annotations = make(map[string]string)
		}
		existingSts.Annotations["blockchain.corp/spec-hash"] = desiredHash

		if err := r.Update(ctx, &existingSts); err != nil {
			log.Error(err, "Failed to update StatefulSet")
			r.Recorder.Event(&ethNode, corev1.EventTypeWarning, "UpdateFailed", err.Error())
			nodeReconcileErrors.WithLabelValues(ethNode.Name).Inc()
			return ctrl.Result{}, err
		}
		r.Recorder.Eventf(&ethNode, corev1.EventTypeNormal, "Updated", "Updated StatefulSet spec hash to %s", desiredHash)
	}

	// 4. Status Update
	// Here we do an extra check: If ActiveReplicas != Spec.Replicas, we are "Degraded"
	newStatus := ethNode.Status.DeepCopy()
	newStatus.ActiveReplicas = existingSts.Status.ReadyReplicas

	// Update only if there is a change to avoid unnecessary API calls
	if ethNode.Status.ActiveReplicas != newStatus.ActiveReplicas {
		ethNode.Status = *newStatus
		if err := r.Status().Update(ctx, &ethNode); err != nil {
			return ctrl.Result{}, err
		}
	}

	// ---------------------------------------------------------
	// 5. CUSTOM MONITORING (Business Logic / SLO Probe)
	// ---------------------------------------------------------

	// List all Pods controlled by this Operator (based on the Label we injected)
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(ethNode.Namespace),
		client.MatchingLabels{"instance": ethNode.Name},
	}

	if err := r.List(ctx, podList, listOpts...); err != nil {
		log.Error(err, "Failed to list pods for metrics collection")
	} else {
		for _, pod := range podList.Items {
			// We only try to connect if the pod is Running and has an assigned IP
			if pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
				// Create the internal cluster URL (Pod-to-Pod communication)
				rpcUrl := fmt.Sprintf("http://%s:8545", pod.Status.PodIP)

				// Call our helper function
				blockHeight, err := r.getBlockHeight(ctx, rpcUrl)

				if err != nil {
					// Log in DEBUG level (V1) to avoid cluttering logs if the node is restarting
					log.V(1).Info("Could not fetch block height", "pod", pod.Name, "error", err)
				} else {
					// SUCCESS! Update the Prometheus Gauge
					nodeBlockHeight.WithLabelValues(ethNode.Name, ethNode.Spec.Network, pod.Name).Set(blockHeight)
					log.V(1).Info("Metric updated", "pod", pod.Name, "block", blockHeight)
				}
			}
		}
	}

	// RequeueAfter: Forces the loop to run every 30s to update metrics,
	// even if nothing changes in K8s. Without this, the metric would stagnate.
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// desiredStatefulSet builds the StatefulSet definition based on the CRD
func (r *EthereumNodeReconciler) desiredStatefulSet(ethNode *infrav1.EthereumNode) (*appsv1.StatefulSet, error) {
	labels := map[string]string{"app": "ethereum-node", "instance": ethNode.Name}

	// Define the name of the JWT secret (fallback to "jwt-secret" if empty)
	jwtSecret := "jwt-secret"
	if ethNode.Spec.JWTSecretName != "" {
		jwtSecret = ethNode.Spec.JWTSecretName
	}

	// --- CONTAINER 1: GETH (EXECUTION) ---
	gethArgs := []string{
		"--http", "--http.addr=0.0.0.0", "--http.vhosts=*",
		"--" + ethNode.Spec.Network,
		"--syncmode=" + ethNode.Spec.SyncMode,
		"--datadir=/data/geth",
		"--authrpc.addr=0.0.0.0",
		"--authrpc.port=8551",
		"--authrpc.vhosts=*",
		"--authrpc.jwtsecret=/secrets/jwt.hex",
	}

	// --- CONTAINER 2: PRYSM (CONSENSUS) ---
	// Note: In production, we would validate if the network is sepolia/mainnet to adjust flags
	prysmArgs := []string{
		"--" + ethNode.Spec.Network,                  // ex: --sepolia
		"--execution-endpoint=http://localhost:8551", // Connect to local Geth
		"--jwt-secret=/secrets/jwt.hex",
		"--datadir=/data/prysm",
		"--accept-terms-of-use",
		"--rpc-host=0.0.0.0",
		"--grpc-gateway-host=0.0.0.0",
	}

	// --- Sync Checkpoint ---
	if ethNode.Spec.CheckpointSyncURL != "" {
		// Prysm needs both flags to work properly with remote checkpoint
		prysmArgs = append(prysmArgs, "--checkpoint-sync-url="+ethNode.Spec.CheckpointSyncURL)
		prysmArgs = append(prysmArgs, "--genesis-beacon-api-url="+ethNode.Spec.CheckpointSyncURL)
	}

	// Parse Storage
	storageQty, err := resource.ParseQuantity(ethNode.Spec.StorageSize)
	if err != nil {
		return nil, fmt.Errorf("invalid storage size: %s", ethNode.Spec.StorageSize)
	}
	var storageClass *string
	if ethNode.Spec.StorageClassName != "" {
		storageClass = &ethNode.Spec.StorageClassName
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ethNode.Name,
			Namespace: ethNode.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &ethNode.Spec.Replicas,
			Selector:    &metav1.LabelSelector{MatchLabels: labels},
			ServiceName: ethNode.Name,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "jwt-secret",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{SecretName: jwtSecret},
							},
						},
					},
					Containers: []corev1.Container{
						// --- GETH ---
						{
							Name:  "execution-client",
							Image: "ethereum/client-go:stable",
							Args:  gethArgs,
							Ports: []corev1.ContainerPort{
								{Name: "rpc", ContainerPort: 8545},
								{Name: "engine", ContainerPort: 8551},
								{Name: "p2p-tcp", ContainerPort: 30303, Protocol: corev1.ProtocolTCP},
								{Name: "p2p-udp", ContainerPort: 30303, Protocol: corev1.ProtocolUDP},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "data", MountPath: "/data", SubPath: "geth"},
								{Name: "jwt-secret", MountPath: "/secrets", ReadOnly: true},
							},
							Resources: ethNode.Spec.Resources,
						},
						// --- PRYSM (SIDECAR) ---
						{
							Name:  "consensus-client",
							Image: "gcr.io/prysmaticlabs/prysm/beacon-chain:stable",
							Args:  prysmArgs,
							Ports: []corev1.ContainerPort{
								{Name: "rpc-prysm", ContainerPort: 4000},
								{Name: "p2p-tcp", ContainerPort: 13000, Protocol: corev1.ProtocolTCP},
								{Name: "p2p-udp", ContainerPort: 12000, Protocol: corev1.ProtocolUDP},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "data", MountPath: "/data", SubPath: "prysm"},
								{Name: "jwt-secret", MountPath: "/secrets", ReadOnly: true},
							},
							Resources: ethNode.Spec.Resources,
						},
					},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "data"},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: storageQty},
					},
					StorageClassName: storageClass,
				},
			}},
		},
	}

	hash, err := calculateHash(sts.Spec)
	if err != nil {
		return nil, err
	}
	if sts.Annotations == nil {
		sts.Annotations = make(map[string]string)
	}
	sts.Annotations["blockchain.corp/spec-hash"] = hash

	if err := ctrl.SetControllerReference(ethNode, sts, r.Scheme); err != nil {
		return nil, err
	}

	return sts, nil
}

// calculateHash generates a SHA256 hash of the StatefulSet Spec
func calculateHash(spec appsv1.StatefulSetSpec) (string, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *EthereumNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.EthereumNode{}).
		Owns(&appsv1.StatefulSet{}). // Controller also watches for changes in the StatefulSet
		Complete(r)
}

// --- STRUCTS AND HELPER FUNCTIONS FOR JSON-RPC ---

type jsonRPCRequest struct {
	Jsonrpc string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type jsonRPCResponse struct {
	Result string      `json:"result"` // Hex string (ex: "0x1b4")
	Error  interface{} `json:"error"`
}

// getBlockHeight conecta no Geth e pede o "eth_blockNumber"
func (r *EthereumNodeReconciler) getBlockHeight(ctx context.Context, url string) (float64, error) {
	payload, _ := json.Marshal(jsonRPCRequest{
		Jsonrpc: "2.0",
		Method:  "eth_blockNumber",
		Params:  []interface{}{},
		ID:      1,
	})

	// Short timeout (2s) to avoid blocking the reconciliation loop
	ctxTimeout, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctxTimeout, "POST", url, bytes.NewBuffer(payload))
	req.Header.Set("Content-Type", "application/json")

	client := http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var rpcResp jsonRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return 0, err
	}

	if rpcResp.Error != nil {
		return 0, fmt.Errorf("rpc error: %v", rpcResp.Error)
	}

	// Converts Hex ("0x10") to Float64
	// If the response is empty or invalid
	if len(rpcResp.Result) < 3 {
		return 0, fmt.Errorf("invalid hex response: %s", rpcResp.Result)
	}

	// Remoes the "0x" prefix and convert
	blockNum, err := strconv.ParseInt(rpcResp.Result[2:], 16, 64)
	if err != nil {
		return 0, err
	}

	return float64(blockNum), nil
}

// reconcileService ensures a Headless Service exists for the StatefulSet
func (r *EthereumNodeReconciler) reconcileService(ctx context.Context, ethNode *infrav1.EthereumNode) error {
	log := log.FromContext(ctx)

	labels := map[string]string{"app": "ethereum-node", "instance": ethNode.Name}

	// 1. Defines the DESIRED object (How we want it to be)
	desiredSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ethNode.Name,
			Namespace: ethNode.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Selector:  labels,
			Ports: []corev1.ServicePort{
				{Name: "rpc-geth", Port: 8545, TargetPort: intstr.FromInt(8545), Protocol: corev1.ProtocolTCP},
				{Name: "engine", Port: 8551, TargetPort: intstr.FromInt(8551), Protocol: corev1.ProtocolTCP},
				{Name: "p2p-geth", Port: 30303, TargetPort: intstr.FromInt(30303), Protocol: corev1.ProtocolTCP},
				{Name: "rpc-prysm", Port: 4000, TargetPort: intstr.FromInt(4000), Protocol: corev1.ProtocolTCP},
				{Name: "p2p-prysm-tcp", Port: 13000, TargetPort: intstr.FromInt(13000), Protocol: corev1.ProtocolTCP},
				{Name: "p2p-prysm-udp", Port: 12000, TargetPort: intstr.FromInt(12000), Protocol: corev1.ProtocolUDP},
			},
		},
	}

	// Configure OwnerReference
	if err := ctrl.SetControllerReference(ethNode, desiredSvc, r.Scheme); err != nil {
		return err
	}

	// 2. Look for the EXISTING object in the cluster
	var existingSvc corev1.Service
	err := r.Get(ctx, types.NamespacedName{Name: ethNode.Name, Namespace: ethNode.Namespace}, &existingSvc)

	if err != nil && apierrors.IsNotFound(err) {
		// Case 1: Does not exist -> CREATE
		log.Info("Creating Headless Service", "Service", desiredSvc.Name)
		return r.Create(ctx, desiredSvc)
	} else if err != nil {
		return err
	}

	// 3. Advanced update logic (Drift Detection)
	updated := false

	// Check A: The Selector changed?
	if !reflect.DeepEqual(existingSvc.Spec.Selector, desiredSvc.Spec.Selector) {
		existingSvc.Spec.Selector = desiredSvc.Spec.Selector
		updated = true
	}

	// Check B: The Ports changed?
	// Note: DeepEqual works well here as long as the order in the array is consistent.
	// If the order is random, we would need a helper function to sort before comparing.
	if !reflect.DeepEqual(existingSvc.Spec.Ports, desiredSvc.Spec.Ports) {
		existingSvc.Spec.Ports = desiredSvc.Spec.Ports
		updated = true
	}

	// 4. Run Update ONLY if necessary
	if updated {
		log.Info("Updating Headless Service ports/selector", "Service", existingSvc.Name)
		// IMPORTANT: We use the modified 'existingSvc' object, as it has the correct ResourceVersion.
		if err := r.Update(ctx, &existingSvc); err != nil {
			return err
		}
	}

	return nil
}

func (r *EthereumNodeReconciler) reconcileP2PService(ctx context.Context, ethNode *infrav1.EthereumNode) error {
	// Different name to avoid conflict with the Headless Service (e.g., ethereumnode-sample-p2p)
	svcName := ethNode.Name + "-p2p"
	labels := map[string]string{"app": "ethereum-node", "instance": ethNode.Name}

	desiredSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: ethNode.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			// LoadBalancer creates a Public IP (if in Cloud) or uses MetalLB (if on-prem)
			Type:     corev1.ServiceTypeLoadBalancer,
			Selector: labels,
			Ports: []corev1.ServicePort{
				// Geth P2P
				{Name: "p2p-geth-tcp", Port: 30303, TargetPort: intstr.FromInt(30303), Protocol: corev1.ProtocolTCP},
				{Name: "p2p-geth-udp", Port: 30303, TargetPort: intstr.FromInt(30303), Protocol: corev1.ProtocolUDP},
				// Prysm P2P
				{Name: "p2p-prysm-tcp", Port: 13000, TargetPort: intstr.FromInt(13000), Protocol: corev1.ProtocolTCP},
				{Name: "p2p-prysm-udp", Port: 12000, TargetPort: intstr.FromInt(12000), Protocol: corev1.ProtocolUDP},
			},
		},
	}

	if err := ctrl.SetControllerReference(ethNode, desiredSvc, r.Scheme); err != nil {
		return err
	}

	// Logic Create-Only to simplify (but Drift Detection would be ideal here as well)
	var existingSvc corev1.Service
	err := r.Get(ctx, types.NamespacedName{Name: svcName, Namespace: ethNode.Namespace}, &existingSvc)
	if err != nil && apierrors.IsNotFound(err) {
		log.FromContext(ctx).Info("Creating P2P LoadBalancer Service", "Service", svcName)
		return r.Create(ctx, desiredSvc)
	}

	return nil
}

func (r *EthereumNodeReconciler) reconcileJWTSecret(ctx context.Context, ethNode *infrav1.EthereumNode) error {
	log := log.FromContext(ctx)

	// 1. Determine the name of the Secret
	secretName := "jwt-secret"
	if ethNode.Spec.JWTSecretName != "" {
		secretName = ethNode.Spec.JWTSecretName
	}

	// 2. Check if it already exists
	var existingSecret corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ethNode.Namespace}, &existingSecret)

	if err == nil {
		// Already exists, do nothing (we don't rotate automatically in this simple example)
		return nil
	}

	if !apierrors.IsNotFound(err) {
		// Real API error (e.g., network failure)
		return err
	}

	// 3. Does not exist: Create it (Generate the Token)
	log.Info("JWT Secret not found. Generating a new secure token...", "Secret", secretName)

	// Generate 32 random bytes
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return err
	}

	// Converts to Hex String (format required by Geth/Prysm)
	hexToken := hex.EncodeToString(token)

	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: ethNode.Namespace,
		},

		// The Kubernetes expects []byte, but the content is the hex string in bytes
		Data: map[string][]byte{
			"jwt.hex": []byte(hexToken),
		},
		Type: corev1.SecretTypeOpaque,
	}

	// 4. Owner Reference (Garbage Collection)
	// This is crucial: If we delete the EthereumNode, K8s deletes the Secret as well.
	if err := ctrl.SetControllerReference(ethNode, newSecret, r.Scheme); err != nil {
		return err
	}

	return r.Create(ctx, newSecret)
}
