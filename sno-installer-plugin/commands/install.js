const { execSync } = require('child_pass'); // Note: In a real environment, this would be 'child_process'
// Wait, I should use 'child_process'. I'll fix it.

const { execSync } = require('child_process');
const path = require('path');

/**
 * Implementation of the /install-sno command.
 *
 * @param {Object} args - The arguments passed to the command.
 * @param {string} args.idrac_pw - The iDRAC password.
 * @param {string} [args.ocp_version] - The OpenShift version.
 */
async function run(args) {
  const { idrac_pw, ocp_version } = args;

  if (!idrac_pw) {
    throw new Error("Missing required argument: idrac_pw");
  }

  // The parent directory contains the Makefile and the installer logic.
  // We assume the plugin is installed in a way that it can access the working directory.
  const workingDir = process.cwd();
  const makeCommand = 'make install';

  const env = {
    ...process.env,
    IDRAC_PW: idrac_pw,
    OCP_VERSION: ocp_version || ''
  };

  console.log(`Starting SNO installation in ${workingDir}...`);
  console.log(`Target OCP Version: ${ocp_version || 'Default'}`);

  try {
    // We use inherit to allow the user to see the progress of the installation in real-time.
    execSync(makeCommand, {
      cwd: workingDir,
      env: env,
      stdio: 'inherit'
    });

    console.log('\n✅ SNO installation completed successfully!');
  } catch (error) {
    console.error('\n❌ SNO installation failed.');
    console.error(error.message);
    throw error;
  }

}

module.exports = { run };
