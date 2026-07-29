import { expect, test, type Page } from '@playwright/test'

async function expectMobileNavigationClosed(page: Page, projectName: string) {
  if (projectName !== 'mobile') return
  await expect(page.locator('.nav-scrim')).toHaveCount(0)
  await expect.poll(async () => {
    const box = await page.locator('.sidebar').boundingBox()
    return box ? Math.round(box.x + box.width) : 0
  }).toBeLessThanOrEqual(0)
}

test('login and core navigation render without overlap', async ({ page }, testInfo) => {
  const email = process.env.TOOLHUB_E2E_EMAIL
  const username = process.env.TOOLHUB_E2E_USERNAME ?? 'admin'
  const localNodeName = process.env.TOOLHUB_E2E_LOCAL_NODE_NAME ?? 'project-host'
  const password = process.env.TOOLHUB_E2E_PASSWORD
  if (!email || !password) throw new Error('TOOLHUB_E2E_EMAIL and TOOLHUB_E2E_PASSWORD are required')
  const consoleErrors: string[] = []
  page.on('console', (message) => { if (message.type() === 'error') consoleErrors.push(message.text()) })

  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'ToolHub' })).toBeVisible()
  await page.getByLabel('Username or email').fill(email)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
  await expect(page.getByText('Tailnet control plane')).toBeVisible()
  if (process.env.TOOLHUB_E2E_EXPECT_PASSWORD_CHANGE === 'true') {
    await expect(page.getByText('This account is using a temporary password.')).toBeVisible()
  }

  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('button', { name: 'Account', exact: true }).click()
  await expectMobileNavigationClosed(page, testInfo.project.name)
  await expect(page.getByRole('heading', { name: 'Account' })).toBeVisible()
  await expect(page.getByLabel('Username')).toHaveValue(username)
  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('button', { name: 'Overview', exact: true }).click()
  await expectMobileNavigationClosed(page, testInfo.project.name)
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()

  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('button', { name: 'Skills', exact: true }).click()
  await expectMobileNavigationClosed(page, testInfo.project.name)
  await expect(page.getByRole('heading', { name: 'Skills' })).toBeVisible()
  await expect(page.getByLabel('Skill provenance')).toBeVisible()
  await expect(page.locator('main')).toHaveCSS('overflow-x', 'visible')

  const viewport = page.viewportSize()!
  const header = await page.locator('.page-header').boundingBox()
  const toolbar = await page.locator('.toolbar').boundingBox()
  expect(header).not.toBeNull()
  expect(toolbar).not.toBeNull()
  expect(header!.y + header!.height).toBeLessThanOrEqual(toolbar!.y + 1)
  expect(header!.x).toBeGreaterThanOrEqual(0)
  expect(header!.x + header!.width).toBeLessThanOrEqual(viewport.width + 1)

  await page.screenshot({ path: `../test-results/${testInfo.project.name}-skills.png`, fullPage: true })

  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('button', { name: 'MCP', exact: true }).click()
  await expectMobileNavigationClosed(page, testInfo.project.name)
  await expect(page.getByRole('heading', { name: 'MCP', exact: true })).toBeVisible()
  await expect(page.getByLabel('MCP provenance')).toBeVisible()
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-mcp.png`, fullPage: true })

  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('navigation').getByRole('button', { name: 'Profiles', exact: true }).click()
  await expectMobileNavigationClosed(page, testInfo.project.name)
  await expect(page.getByRole('heading', { name: 'Profiles' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Add Profile' })).toBeVisible()
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-profiles.png`, fullPage: true })

  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('button', { name: 'Runtime View', exact: true }).click()
  await expectMobileNavigationClosed(page, testInfo.project.name)
  await expect(page.getByRole('heading', { name: 'Runtime View' })).toBeVisible()
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-runtime-view.png`, fullPage: true })

  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('button', { name: 'Nodes', exact: true }).click()
  await expectMobileNavigationClosed(page, testInfo.project.name)
  await expect(page.getByRole('heading', { name: 'Nodes' })).toBeVisible()
  await expect(page.getByText('Project host', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Configure pinned SSH fallback' }).first().click()
  await expect(page.getByRole('heading', { name: /SSH fallback/ })).toBeVisible()
  await expect(page.getByLabel('Pinned known_hosts line')).toBeVisible()
  await expect(page.getByLabel('Private key')).toBeVisible()
  await page.getByRole('button', { name: 'Close', exact: true }).click()
  await page.getByRole('button', { name: 'Enroll project host' }).click()
  await expect(page.getByRole('heading', { name: /Enroll project host/ })).toBeVisible()
  await expect(page.getByLabel('Node name')).toHaveValue(localNodeName)
  await page.getByRole('button', { name: 'Close', exact: true }).click()
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-nodes.png`, fullPage: true })
  await page.getByRole('button', { name: 'Sign out' }).click()
  await page.getByLabel('Username or email').fill(username)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
  expect(consoleErrors).toEqual([])
})

test('viewer can inspect Profiles and Runtime View without mutation controls', async ({ page }, testInfo) => {
  const node = { id: '11111111-1111-4111-8111-111111111111', name: 'viewer-node', status: 'online', isLocal: true, runtimeKinds: ['codex'], activations: [{ runtime: 'codex', profileId: '22222222-2222-4222-8222-222222222222', profileName: 'Research', state: 'active' }] }
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/session') {
      await route.fulfill({ json: { authenticated: true, csrfToken: 'viewer-csrf', user: { id: '33333333-3333-4333-8333-333333333333', username: 'viewer', email: 'viewer@toolhub.local', displayName: 'Viewer', roles: ['viewer'] } } })
      return
    }
    if (path === '/api/v1/profiles') {
      await route.fulfill({ json: { items: [{ id: '22222222-2222-4222-8222-222222222222', name: 'Research', description: 'Read-only fixture', mcpServerCount: 1, skillCount: 1, activationCount: 1 }] } })
      return
    }
    if (path === '/api/v1/nodes') {
      await route.fulfill({ json: { items: [node] } })
      return
    }
    if (path === '/api/v1/skills' || path === '/api/v1/mcp/servers') {
      await route.fulfill({ json: { items: [] } })
      return
    }
    if (path === `/api/v1/targets/${node.id}/codex`) {
      await route.fulfill({ json: { node, runtime: 'codex', capabilities: { skills: true, mcp: true, mcpNote: '' }, activation: { profileId: '22222222-2222-4222-8222-222222222222', profileName: 'Research', previousProfileId: '', previousProfileName: '', state: 'active', lastError: '', skipped: [], activatedAt: '2026-07-28T00:00:00Z', activatedBy: 'admin' }, mcp: { mcpmProfile: 'toolhub-codex', deploymentId: '44444444-4444-4444-8444-444444444444', state: 'in_sync', servers: [] }, skills: [], drift: { mcp: 0, skills: 0 } } })
      return
    }
    await route.fulfill({ status: 404, json: { error: { code: 'not_found', message: path } } })
  })

  await page.goto('/profiles')
  await expect(page.getByRole('heading', { name: 'Profiles' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Add Profile' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Activate Profile' })).toHaveCount(0)
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-viewer-profiles.png`, fullPage: true })

  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('button', { name: 'Runtime View', exact: true }).click()
  await expectMobileNavigationClosed(page, testInfo.project.name)
  await expect(page.getByRole('heading', { name: 'Runtime View' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Active Profile' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Retry activation' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Deactivate Profile' })).toHaveCount(0)
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-viewer-runtime-view.png`, fullPage: true })
})

test('operator confirms named secrets before remote Profile activation', async ({ page }) => {
  const profileID = '22222222-2222-4222-8222-222222222222'
  const nodeID = '11111111-1111-4111-8111-111111111111'
  let activationBody: { confirmSecrets?: boolean } | null = null
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/session') {
      await route.fulfill({ json: { authenticated: true, csrfToken: 'operator-csrf', user: { id: '33333333-3333-4333-8333-333333333333', username: 'operator', email: 'operator@toolhub.local', displayName: 'Operator', roles: ['operator'] } } })
      return
    }
    if (path === '/api/v1/profiles' && request.method() === 'GET') {
      await route.fulfill({ json: { items: [{ id: profileID, name: 'Research', description: 'Remote fixture', mcpServerCount: 1, skillCount: 0, activationCount: 0 }] } })
      return
    }
    if (path === '/api/v1/skills' || path === '/api/v1/mcp/servers') {
      await route.fulfill({ json: { items: [] } })
      return
    }
    if (path === '/api/v1/nodes') {
      await route.fulfill({ json: { items: [{ id: nodeID, name: 'remote-build-node', status: 'online', isLocal: false, runtimeKinds: ['codex'], activations: [] }] } })
      return
    }
    if (path === `/api/v1/profiles/${profileID}/preflight`) {
      await route.fulfill({ status: 409, json: { error: { code: 'remote_secret_confirmation_required', message: 'Confirm named secrets', issues: [{ code: 'remote_secret_confirmation_required', scope: 'mcp', detail: 'Confirm the named secret keys before remote delivery' }], skipped: [], nodeName: 'remote-build-node', secretKeys: ['TAVILY_API_KEY'] } } })
      return
    }
    if (path === `/api/v1/profiles/${profileID}/activate`) {
      activationBody = request.postDataJSON()
      await route.fulfill({ status: 202, json: { id: '44444444-4444-4444-8444-444444444444', kind: 'profile_activate', state: 'pending' } })
      return
    }
    await route.fulfill({ status: 404, json: { error: { code: 'not_found', message: path } } })
  })

  await page.goto('/profiles')
  await page.getByRole('button', { name: 'Activate Profile' }).click()
  await expect(page.getByRole('dialog', { name: 'Activate Profile · Research' })).toBeVisible()
  await page.getByRole('button', { name: 'Run preflight' }).click()
  await expect(page.getByText('TAVILY_API_KEY')).toBeVisible()
  await page.getByRole('button', { name: 'Review secret keys' }).click()
  await expect(page.getByRole('dialog', { name: 'Confirm remote secret delivery' })).toContainText('remote-build-node')
  await page.getByRole('button', { name: 'Confirm and activate' }).click()
  await expect(page.getByText('Profile activation queued.')).toBeVisible()
  expect(activationBody?.confirmSecrets).toBe(true)
})
