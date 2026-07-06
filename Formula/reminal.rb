class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.5.1"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.5.1/reminal_1.5.1_darwin_arm64.tar.gz"
      sha256 "775d04156ec77c83c2b921986dcac2a4960f67eca98ff7d6794734b5431e74d4"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.5.1/reminal_1.5.1_darwin_amd64.tar.gz"
      sha256 "deca2d5500f64c2774e31fac3aaff0784f2899b261086de9a5c4dc9b5021b23c"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.5.1/reminal_1.5.1_linux_arm64.tar.gz"
      sha256 "065ea4c0d7ab01912f4a5febf808faf5dd24e44d5792b08e2f14f21f17bd6427"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.5.1/reminal_1.5.1_linux_amd64.tar.gz"
      sha256 "c47f0fb040022d721880152b13486ff306960f9a19e012c54448d0f5121dd750"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
