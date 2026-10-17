# Third-party benchmark data

This tool includes a 24-question subset selected from previously downloaded benchmark data. No upstream evaluator code is executed. Prompts add a final-answer formatting instruction; expected answers are retained only for local grading.

- BIG-Bench Hard: Suzgun et al., "Challenging BIG-Bench Tasks and Whether Chain-of-Thought Can Solve Them" (2022). Source: https://github.com/suzgunmirac/BIG-Bench-Hard . Repository license: MIT. The bundled BBH_LICENSE.txt preserves its notice. 12 selected examples.
- BIG-Bench Extra Hard: Google DeepMind / Google LLC (2025). Source: https://github.com/google-deepmind/bbeh at commit 80d12ca916b7158f22293fcf3144f4d3d854d4be. Non-software materials are CC BY 4.0, https://creativecommons.org/licenses/by/4.0/ . Software is Apache 2.0; no evaluator code is redistributed here. 4 selected examples, with formatting instructions added. Not an official Google product.
- MATH-500: 8 selected questions from OpenAI simple-evals' published math_500_test.csv. Dataset source: https://openaipublic.blob.core.windows.net/simple-evals/math_500_test.csv . Evaluation repository: https://github.com/openai/simple-evals . Original MATH benchmark: Hendrycks et al., "Measuring Mathematical Problem Solving With the MATH Dataset" (2021), https://github.com/hendrycks/math . The original source license is preserved as MATH_LICENSE.txt; simple-evals' software license is not asserted to replace data-specific terms.

The per-question source URL and original index are in community.json. source-hashes.json identifies the full source files used for selection. This is a small custom zero-shot evaluation, not a reproduction of the full benchmark or an authentication test for DeepSeek model identity.
