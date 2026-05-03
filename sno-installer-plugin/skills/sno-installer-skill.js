/**
 * Implementation of the sno-installer-sskill.
 * This skill acts as a specialized agent for SNO lifecycle management.
 */

const { execSync } = require('child_process');

/**
 * The skill implementation.
 *
 * @param {Object} context - The context of the skill invocation.
 * @param {string} context.action - The action to perform (e.g., 'preflight', 'build-iso', 'deploy', 'status').
 * @param {Object} context.args - Arguments for the action.
 * @returns {Promise<string>} - A summary of the action's result.
 */
async function handleSkill(context) {
  const { action, args } = context;
  const workingDir = process.cwd();
  const env = { ...process.env, ...args };

  const target = action; // Mapping action directly to Makefile target
  const makeCommand = `make ${target}`;

  console.log(`[SNO Skill] Executing action: ${action}`);

  try {
    execSync(makeCommand, {
      cwd: workingDir,
      env: env,
      stdio: 'inherit'
    });

    return `Successfully completed action: ${action}`;
  } catch (error) {
    const errorMessage = `Failed to execute action: ${action}. Error: ${error.message}`;
    console.error(`[SNO Skill] ${errorMessage}`);
    throw new Error(errorMessage);
  }
}

module.exports = { handleSkill };
