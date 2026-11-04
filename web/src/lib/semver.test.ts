import { describe, expect, it } from 'vitest'
import { compareVersions } from './semver'

describe('compareVersions', () => {
  it.each([
    ['1.3.5', '1.3.5', 0],
    ['1.3.5', '1.3.4', 1],
    ['1.2.10', '1.2.9', 1],
    ['1.3', '1.3.0', 0],
    ['v1.4.0', '1.3.9', 1],
    ['1.3.5-beta', '1.3.5', -1],
    ['1.3.5', '1.3.5-rc1', 1],
    ['0.9', '1.0', -1],
    ['', '0.0.1', -1],
  ])('%s vs %s', (a, b, want) => {
    expect(compareVersions(a, b)).toBe(want)
  })
})
