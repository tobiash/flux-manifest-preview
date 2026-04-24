import * as core from '@actions/core'
import * as exec from '@actions/exec'
import * as github from '@actions/github'
import * as tc from '@actions/tool-cache'
import fs from 'fs'
import os from 'os'
import path from 'path'
import {fileURLToPath} from 'url'

const commentMarker = '<!-- fmp-comment-marker -->'
const moduleDir = path.dirname(fileURLToPath(import.meta.url))

async function run() {
  let exitCode = 0
  let execError = null

  try {
    const binaryPath = await resolveBinaryPath()

    await core.group('Run fmp github-action', async () => {
      core.info(`Using fmp binary: ${binaryPath}`)
      try {
        exitCode = await exec.exec(binaryPath, ['github-action'], {
          ignoreReturnCode: true
        })
      } catch (err) {
        execError = err
      }
	    })

	    const report = readReport()
	    await maybeSyncComment(report)
	    try {
	      await maybeSyncLabels(report)
	    } catch (err) {
	      core.warning(`PR label sync failed: ${err instanceof Error ? err.message : String(err)}`)
	    }

    if (execError) {
      throw execError
    }
    if (exitCode !== 0) {
      throw new Error(`fmp github-action failed with exit code ${exitCode}`)
    }
  } catch (err) {
    core.setFailed(err instanceof Error ? err.message : String(err))
  }
}

async function resolveBinaryPath() {
  const customBinary = core.getInput('binary').trim()
  if (customBinary !== '') {
    return ensureBinary(resolveInputPath(customBinary))
  }

  const version = resolveActionVersion()
  if (!/^v.+/.test(version)) {
    throw new Error('This action no longer builds fmp from source. For non-release refs, provide `with: binary:`.')
  }

  const toolArch = process.arch
  const cachedDir = tc.find('fmp', version, toolArch)
  if (cachedDir !== '') {
    return ensureBinary(path.join(cachedDir, binaryName()))
  }

  return core.group(`Download fmp ${version}`, async () => {
    const archivePath = await tc.downloadTool(releaseURL(version))
    const extractedDir = await tc.extractTar(archivePath)
    const installedDir = await tc.cacheDir(extractedDir, 'fmp', version, toolArch)
    return ensureBinary(path.join(installedDir, binaryName()))
  })
}

function resolveActionVersion() {
  const envOverride = (process.env.FMP_ACTION_REF || '').trim()
  if (envOverride !== '') {
    return envOverride
  }
  return path.basename(path.resolve(moduleDir, '..'))
}

function releaseURL(version) {
  const platform = platformName(process.platform)
  const arch = archiveArch(process.arch)
  return `https://github.com/tobiash/flux-manifest-preview/releases/download/${version}/fmp_${version}_${platform}_${arch}.tar.gz`
}

function resolveInputPath(inputPath) {
  if (path.isAbsolute(inputPath)) {
    return inputPath
  }

  const workspace = process.env.GITHUB_WORKSPACE || process.cwd()
  return path.resolve(workspace, inputPath)
}

function ensureBinary(binaryPath) {
  if (!fs.existsSync(binaryPath)) {
    throw new Error(`fmp binary not found at ${binaryPath}`)
  }

  if (process.platform !== 'win32') {
    fs.chmodSync(binaryPath, 0o755)
  }

  return binaryPath
}

function readReport() {
  const reportPath = path.join(process.env.RUNNER_TEMP || os.tmpdir(), 'fmp-report.json')
  if (!fs.existsSync(reportPath)) {
    return null
  }

  return JSON.parse(fs.readFileSync(reportPath, 'utf8'))
}

async function maybeSyncComment(report) {
  if (!booleanInput('comment', false)) {
    return
  }
  if (github.context.eventName !== 'pull_request') {
    core.info('Skipping PR comment sync outside pull_request events')
    return
  }
  if (!report) {
    core.warning('Skipping PR comment sync because fmp-report.json was not written')
    return
  }

  const token = stringInput('github-token', '')
  if (token === '') {
    core.warning('Skipping PR comment sync because github-token is empty')
    return
  }
  core.setSecret(token)

  const octokit = github.getOctokit(token)
  const {owner, repo} = github.context.repo
  const issueNumber = github.context.issue.number
  if (!issueNumber) {
    core.warning('Skipping PR comment sync because pull request context is missing issue number')
    return
  }

  const comments = await octokit.paginate(octokit.rest.issues.listComments, {
    owner,
    repo,
    issue_number: issueNumber,
    per_page: 100
  })
  const existingComment = comments.find(comment => typeof comment.body === 'string' && comment.body.includes(commentMarker))

  if (report.status === 'clean' && stringInput('comment-mode', 'changes') === 'changes') {
    if (existingComment) {
      await octokit.rest.issues.deleteComment({
        owner,
        repo,
        comment_id: existingComment.id
      })
    }
    return
  }

  const commentPath = report.comment_file || path.join(process.env.RUNNER_TEMP || os.tmpdir(), 'fmp-comment.md')
  if (!fs.existsSync(commentPath)) {
    return
  }

  const body = fs.readFileSync(commentPath, 'utf8')
  if (existingComment) {
    await octokit.rest.issues.updateComment({
      owner,
      repo,
      comment_id: existingComment.id,
      body
    })
    return
  }

  await octokit.rest.issues.createComment({
    owner,
    repo,
    issue_number: issueNumber,
    body
  })
}

async function maybeSyncLabels(report) {
  if (github.context.eventName !== 'pull_request') {
    return
  }
  if (!report || !Array.isArray(report.labels) || report.labels.length === 0) {
    return
  }

  const token = stringInput('github-token', '')
  if (token === '') {
    core.warning('Skipping PR label sync because github-token is empty')
    return
  }

  const octokit = github.getOctokit(token)
  const {owner, repo} = github.context.repo
  const issueNumber = github.context.issue.number
  if (!issueNumber) {
    core.warning('Skipping PR label sync because pull request context is missing issue number')
    return
  }

  await octokit.rest.issues.addLabels({
    owner,
    repo,
    issue_number: issueNumber,
    labels: report.labels
  })
}

function platformName(platform) {
  switch (platform) {
    case 'linux':
      return 'linux'
    case 'darwin':
      return 'darwin'
    default:
      throw new Error(`unsupported runner platform: ${platform}`)
  }
}

function archiveArch(arch) {
  switch (arch) {
    case 'x64':
      return 'amd64'
    case 'arm64':
      return 'arm64'
    default:
      throw new Error(`unsupported runner architecture: ${arch}`)
  }
}

function binaryName() {
  if (process.platform === 'win32') {
    return 'fmp.exe'
  }
  return 'fmp'
}

function stringInput(name, defaultValue) {
  const value = core.getInput(name).trim()
  if (value === '') {
    return defaultValue
  }
  return value
}

function booleanInput(name, defaultValue) {
  const value = stringInput(name, '')
  if (value === '') {
    return defaultValue
  }
  return value.toLowerCase() === 'true'
}

void run()
