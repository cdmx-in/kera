import { expect, it } from 'vitest'
import { fileAttach } from './attach'

it('keeps a collection a collection and sends images and files as files', () => {
  expect(fileAttach({ id: 'k1', type: 'collection' })).toEqual({ type: 'collection', id: 'k1' })
  expect(fileAttach({ id: 'f1', type: 'image' })).toEqual({ type: 'file', id: 'f1' })
  expect(fileAttach({ id: 'f2' })).toEqual({ type: 'file', id: 'f2' })
})
