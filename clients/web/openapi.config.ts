import type { GeneratorConfig } from 'ng-openapi';

const config: GeneratorConfig = {
  input: './../../api/openapi.yaml',
  output: './src/app/api',
  options: {
    dateType: 'Date',
    enumStyle: 'enum',
    generateServices: true,
  },
};

export default config;
