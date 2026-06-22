class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.4.2"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.4.2/reminal_0.4.2_darwin_arm64.tar.gz"
      sha256 "49a2171801644c052be0685ed9faecece4df47ee9d3cc4d12607cf20f40d68b3"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.4.2/reminal_0.4.2_darwin_amd64.tar.gz"
      sha256 "87a54b6a20eb449f04b6d85a0efb39b50fbd81f43d8a5d109f8ac12ac812f847"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.4.2/reminal_0.4.2_linux_arm64.tar.gz"
      sha256 "1cc8eeeb468c89d4af328638413f7e2045a2423ff7c165db12bef6c1ddfaa8d5"
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
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.reminal.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.reminal.workers.dev"
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
