#!/bin/bash
set -euo pipefail

# Define paths
WORKDIR="./workdir"
SRC="./abi-master-0"

# iDRAC details
IDRAC_IP="192.168.1.228"
IDRAC_ID="root"

# Decrypt password at runtime
read -s -p "Enter passphrase to decrypt iDRAC password: " PASSPHRASE
IDRAC_PW=$(openssl enc -aes-256-cbc -d -in idrac_pw.enc -pass pass:"$PASSPHRASE")

# ISO URL source
ISO="${WORKDIR}/agent.x86_64.iso"
REMOTE_USER="rock"
REMOTE_HOST="192.168.1.21"
REMOTE_PATH="/apps/webcache/OSs/"
ISO_URL="http://${REMOTE_HOST}:8080/OSs/agent.x86_64.iso"

INSTALLER="./openshift-install"

# Define OCP version
OCP_VERSION="4.16.45"

# Variables
SSH_KEY="$HOME/.ssh/id_ed25519.pub"

# Check if the SSH key exists
if [ ! -f "$SSH_KEY" ]; then
    echo "SSH key not found. Generating a new ed25519 key..."
    echo "Generating SSH key at $SSH_KEY ..."
    ssh-keygen -t ed25519 -f "${SSH_KEY%.*}" -N "" -q
    echo "SSH key generated."
fi

# Share the SSH key with the remote host
echo "============================================================================================"
echo "Copying SSH key to $REMOTE_USER@$REMOTE_HOST ..."
sshpass -p "$IDRAC_PW" ssh-copy-id -i "$SSH_KEY" -o StrictHostKeyChecking=no "$REMOTE_USER@$REMOTE_HOST"
echo "SSH key copied to $REMOTE_USER@$REMOTE_HOST"
echo "============================================================================================"

# Path to Docker auth file
REGISTRY_AUTH_FILE="./config.json"

RELEASE_DIGEST=$(oc adm release info quay.io/openshift-release-dev/ocp-release:${OCP_VERSION}-x86_64 \
  --registry-config ${REGISTRY_AUTH_FILE} | awk '/Pull From:/ {print $3}')
echo "============================================================================================"

# Print to verify
echo "RELEASE_DIGEST=${RELEASE_DIGEST}"
echo "============================================================================================"


# Generate and run the command
CMD="oc adm release extract -a ${REGISTRY_AUTH_FILE} --command=openshift-install ${RELEASE_DIGEST}"

echo "Running: $CMD"
$CMD
echo "============================================================================================"

# If workdir exists, clean it
if [ -d "$WORKDIR" ]; then
  echo "Cleaning existing $WORKDIR ..."
  rm -rf "${WORKDIR}"
  rm -rf "${WORKDIR}/.*"
else
  echo "Creating $WORKDIR ..."
  mkdir -p "$WORKDIR"
fi

# Copy openshift directory
if [ -d "$SRC/openshift" ]; then
  echo "Copying $SRC/openshift -> $WORKDIR/ ..."
  mkdir -p "$WORKDIR"
  cp -r "$SRC/openshift" "$WORKDIR/"
else
  echo "ERROR: $SRC/openshift not found!" >&2
  exit 1
fi

# Copy yaml files
for FILE in agent-config.yaml install-config.yaml; do
  if [ -f "$SRC/$FILE" ]; then
    echo "Copying $SRC/$FILE -> $WORKDIR/ ..."
    cp "$SRC/$FILE" "$WORKDIR/"
  else
    echo "ERROR: $SRC/$FILE not found!" >&2
    exit 1
  fi
done

echo "✅ Done! Files are prepared in $WORKDIR/"
echo "============================================================================================"

# Run the openshift-install command
if [ -x "$INSTALLER" ]; then
  echo "Running: $INSTALLER agent create image --dir $WORKDIR/ --log-level debug"
  echo "============================================================================================"
  "$INSTALLER" agent create image --dir "$WORKDIR/" --log-level debug
  echo "============================================================================================"
else
  echo "ERROR: $INSTALLER not found or not executable!" >&2
  exit 1
fi

# Check if ISO exists and scp it
if [ -f "$ISO" ]; then
  echo "ISO found: $ISO"
  echo "Copying to $REMOTE_USER@$REMOTE_HOST:$REMOTE_PATH ..."
  scp -r "$ISO" "$REMOTE_USER@$REMOTE_HOST:$REMOTE_PATH"
  echo "✅ ISO successfully copied."
else
  echo "⚠️  ISO not found: $ISO"
fi


echo "✅ Done for generating the agent.x86_64.iso!"
echo "============================================================================================"


# Eject virtual media (iDRAC8)
echo "Ejecting virtual media..."
curl -sku "$IDRAC_ID:$IDRAC_PW" \
  -H "Content-Type: application/json" \
  -X POST \
  -d '{}' \
  https://${IDRAC_IP}/redfish/v1/Managers/iDRAC.Embedded.1/VirtualMedia/CD/Actions/VirtualMedia.EjectMedia | jq .


sleep 20

# Mount (insert) ISO in iDRAC8
echo "Inserting ISO image..."
curl -sku "$IDRAC_ID:$IDRAC_PW" \
  -H "Content-Type: application/json" \
  -X POST \
  -d '{"Image": "http://192.168.1.21:8080/OSs/agent.x86_64.iso"}' \
  https://${IDRAC_IP}/redfish/v1/Managers/iDRAC.Embedded.1/VirtualMedia/CD/Actions/VirtualMedia.InsertMedia | jq .

# Set next boot to Virtual CD/DVD
echo "Setting next boot to Virtual CD/DVD..."
curl -sku "$IDRAC_ID:$IDRAC_PW" \
  -H "Content-Type: application/json" \
  -X PATCH \
  -d '{
        "Boot": {
          "BootSourceOverrideTarget": "Cd",
          "BootSourceOverrideEnabled": "Once"
        }
      }' \
  https://${IDRAC_IP}/redfish/v1/Systems/System.Embedded.1 | jq .


## Set One-Time Boot to Virtual CD/DVD
#echo "Setting one-time boot to virtual CD/DVD..."
#curl -sku "$IDRAC_ID:$IDRAC_PW" \
#  -H "Content-Type: application/json" \
#  -X PATCH \
#  -d '{"Boot":{"BootSourceOverrideEnabled":"Once","BootSourceOverrideTarget":"Cd"}}' \
#  https://${IDRAC_IP}/redfish/v1/Systems/System.Embedded.1 | jq .
#
# Power cycle / restart server
echo "Restarting server..."
curl -sku "$IDRAC_ID:$IDRAC_PW" \
  -H "Content-Type: application/json" \
  -X POST \
  -d '{"ResetType":"ForceRestart"}' \
  https://${IDRAC_IP}/redfish/v1/Systems/System.Embedded.1/Actions/ComputerSystem.Reset | jq .

# --- Check iDRAC power status ---
echo "Checking power status of server at $IDRAC_IP ..."
POWER_STATE=$(curl -sku "$IDRAC_ID:$IDRAC_PW" -H "Content-Type: application/json" \
  https://${IDRAC_IP}/redfish/v1/Systems/System.Embedded.1 2>/dev/null | jq -r '.PowerState')

if [ "$POWER_STATE" == "On" ]; then
  echo "✅ Server is powered ON. Running wait-for install-complete ..."
  export KUBECONFIG=$(pwd)/workdir/auth/kubeconfig
  "$INSTALLER" agent wait-for install-complete --dir "$WORKDIR"
else
  echo "⚠️ Server power state is: $POWER_STATE"
  echo "Skipping wait-for install-complete."
fi
echo "✅ Done!"