import { DEFAULT_STACK_NAME, MAX_STACK_NAME_LENGTH, parseContext } from '../lib/context';

const parse = (values: Record<string, unknown>) => parseContext((key) => values[key]);

describe('parseContext', () => {
  test('defaults', () => {
    const { stackName, props } = parse({});
    expect(stackName).toBe(DEFAULT_STACK_NAME);
    expect(props.logRetentionDays).toBeUndefined();
    expect(props.deleteLogsOnDestroy).toBeUndefined();
  });

  test('flags and numbers', () => {
    const { stackName, props } = parse({ stackName: 'rt-server', logRetentionDays: '90', deleteLogsOnDestroy: 'false', scoreToWin: '3' });
    expect(stackName).toBe('rt-server');
    expect(props).toMatchObject({ logRetentionDays: 90, deleteLogsOnDestroy: false, scoreToWin: 3 });
  });

  test('allowedOrigins', () => {
    const { props } = parse({ allowedOrigins: 'http://127.0.0.1:8080, https://play.example.com' });
    expect(props.allowedOrigins).toEqual(['http://127.0.0.1:8080', 'https://play.example.com']);
  });

  test('fleetType=ec2 needs a narrow allowedCidr', () => {
    expect(parse({}).props.fleetType).toBe('anywhere');
    expect(parse({ fleetType: 'ec2', allowedCidr: '203.0.113.7/32' }).props).toMatchObject({ fleetType: 'ec2', allowedCidr: '203.0.113.7/32' });
    expect(() => parse({ fleetType: 'ec2' })).toThrow('allowedCidr');
    expect(() => parse({ fleetType: 'ec2', allowedCidr: '0.0.0.0/0' })).toThrow('every address');
    expect(parse({ fleetType: 'ec2', allowedCidr: '0.0.0.0/0', allowPublic: 'true' }).props.allowedCidr).toBe('0.0.0.0/0');
    expect(() => parse({ fleetType: 'ec2', allowedCidr: '300.1.1.1/32' })).toThrow('allowedCidr');
    expect(() => parse({ fleetType: 'ec2', allowedCidr: '::/0' })).toThrow('allowedCidr');
    expect(() => parse({ allowedCidr: '203.0.113.7/32' })).toThrow('fleetType=ec2');
    expect(() => parse({ fleetType: 'containers' })).toThrow('fleetType');
  });

  test('accepts non-string values set in cdk.json', () => {
    const { props } = parse({ logRetentionDays: 14, deleteLogsOnDestroy: true });
    expect(props).toMatchObject({ logRetentionDays: 14, deleteLogsOnDestroy: true });
  });

  test.each([
    ['logRetentionDays', 'abc'],
    ['logRetentionDays', '0'],
    ['logRetentionDays', '1.5'],
    ['logRetentionDays', '3651'],
    ['logRetentionDays', '45'],
    ['scoreToWin', '0'],
    ['scoreToWin', '21'],
    ['scoreToWin', 'many'],
    ['allowedOrigins', '*'],
    ['allowedOrigins', 'http://127.0.0.1:8080/path'],
    ['allowedOrigins', 'ftp://example.com'],
    ['allowedOrigins', ','],
    ['deleteLogsOnDestroy', 'yes'],
    ['deleteLogsOnDestroy', '1'],
    ['stackName', '1bad'],
    ['stackName', 'has_underscore'],
    ['stackName', 'a'.repeat(MAX_STACK_NAME_LENGTH + 1)],
  ])('rejects %s=%s and names the key', (key, value) => {
    expect(() => parse({ [key]: value })).toThrow(key);
  });
});
