#!/bin/bash

# Usage: ./generate-gitopsaddon.sh
# This script generates a YAML file with gitops-addon ManagedClusterAddOn configurations for vm00001 to vm02673.
# The output file is gitopsaddons.yaml to be used in the gitopsaddon large scale e2e test.

OUTPUT_FILE="gitopsaddons.yaml"
MAX_NAMESPACE_COUNT=2673

# Clear the output file if it already exists
> "$OUTPUT_FILE"

for i in $(seq 1 "$MAX_NAMESPACE_COUNT"); do
    # Format the number with leading zeros (e.g., 1 -> 00001, 10 -> 00010)
    # printf "%05d" ensures a minimum width of 5, padded with zeros
    NAMESPACE_NUM=$(printf "%05d" "$i")
    NAMESPACE="vm${NAMESPACE_NUM}"

    YAML_TEMPLATE=$(cat <<EOF
apiVersion: addon.open-cluster-management.io/v1alpha1
kind: ManagedClusterAddOn
metadata:
  name: gitops-addon
  namespace: ${NAMESPACE}
spec:
  installNamespace: open-cluster-management-agent-addon
EOF
)

    echo "$YAML_TEMPLATE" >> "$OUTPUT_FILE"

    if [ "$i" -lt "$MAX_NAMESPACE_COUNT" ]; then
        echo "---" >> "$OUTPUT_FILE"
    fi
done

echo "Generated '$OUTPUT_FILE' with gitops-addon ManagedClusterAddOn configurations for vm00001 to vm$(printf "%05d" "$MAX_NAMESPACE_COUNT")."
